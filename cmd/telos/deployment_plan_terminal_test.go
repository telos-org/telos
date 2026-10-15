package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInteractiveApplyShowsThePlanWhenStdoutIsRedirected(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("PTY regression requires Python 3 on Unix")
	}
	var confirms atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		request := testDeploymentPlan("apply", "awaiting_confirmation")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/deployments/sess_123":
			_, _ = w.Write([]byte(`{"id":"sess_123","current_revision_id":"rev_7"}`))
			return
		case "POST /api/deployment-plans":
			request.Status, request.Preview = "queued", nil
		case "GET /api/deployments/sess_123/change-requests/cr_saved":
		case "POST /api/deployments/sess_123/change-requests/cr_saved/confirm":
			confirms.Add(1)
			request.Status, request.Preview = "applied", nil
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	path := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(path, []byte(testPlanSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal([]string{"apply", path, "--goal", "sess_123", "--message", "Update the demo"})
	// The child has a real terminal for input and prompts, but stdout is a pipe.
	// Capture what was visible before answering, not the redirected receipt.
	command := exec.Command(python, "-c", `
import json, os, pty, select, subprocess, sys, time
master, slave = pty.openpty()
process = subprocess.Popen([sys.argv[1], "-test.run=^TestCLIPlanApplySubprocess$"], stdin=slave, stderr=slave, stdout=subprocess.PIPE)
os.close(slave)
visible = b""
try:
    deadline = time.monotonic() + 10
    while b"Type yes to confirm:" not in visible:
        if time.monotonic() >= deadline:
            raise RuntimeError("confirmation prompt did not arrive: " + repr(visible))
        if select.select([master], [], [], 0.1)[0]:
            visible += os.read(master, 65536)
    os.write(master, b"yes\n")
    output, _ = process.communicate(timeout=10)
    print(json.dumps({"visible": visible.decode(), "output": output.decode(), "code": process.returncode}))
finally:
    if process.poll() is None:
        process.kill()
        process.wait()
    os.close(master)
`, os.Args[0])
	command.Env = append(os.Environ(), "TELOS_TEST_PLAN_COMMAND="+string(args))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("PTY command: %v\n%s", err, output)
	}
	var receipt struct {
		Visible, Output string
		Code            int
	}
	if err := json.Unmarshal(output, &receipt); err != nil {
		t.Fatalf("PTY receipt: %v\n%s", err, output)
	}
	if receipt.Code != 0 || confirms.Load() != 1 || !strings.Contains(receipt.Visible, "+Serve an updated demo.") || !strings.Contains(receipt.Output, "applied") {
		t.Fatalf("approval hid the plan or failed: %+v confirmations=%d", receipt, confirms.Load())
	}
}
