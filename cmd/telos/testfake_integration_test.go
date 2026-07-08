package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIWithTestFakeExecutor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in short mode")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	telosBin := filepath.Join(binDir, "telos")
	telosdBin := filepath.Join(binDir, "telosd")
	runProcess(t, repoRoot, nil, "go", "build", "-tags", "telos_testfake", "-o", telosBin, "./cmd/telos")
	runProcess(t, repoRoot, nil, "go", "build", "-tags", "telos_testfake", "-o", telosdBin, "./cmd/telosd")

	workspace := t.TempDir()
	writeFakeIntegrationSpec(t, workspace)
	writeFakeScenario(t, workspace)
	initFakeIntegrationGitRepo(t, workspace)

	env := []string{
		"TELOSD_PATH=" + telosdBin,
	}
	runOut := runProcess(t, workspace, env, telosBin, "run", "SPEC.md", "--json", "--max-cost-usd", "1")
	var runResp struct {
		SessionID  string `json:"session_id"`
		SessionDir string `json:"session_dir"`
		SpecName   string `json:"spec_name"`
		Status     string `json:"status"`
	}
	mustJSON(t, runOut, &runResp)
	if runResp.SessionID == "" || runResp.SessionDir == "" {
		t.Fatalf("run response missing session fields:\n%s", runOut)
	}
	if runResp.SpecName != "fake-cli" || runResp.Status != "running" {
		t.Fatalf("unexpected run response: %+v", runResp)
	}

	waitForFakeSessionStatus(t, workspace, env, telosBin, runResp.SessionID, "completed")

	listOut := runProcess(t, workspace, env, telosBin, "list", "--local", "--wide", "--json")
	var listResp struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	mustJSON(t, listOut, &listResp)
	listed := false
	for _, session := range listResp.Sessions {
		if session.SessionID == runResp.SessionID {
			listed = true
			break
		}
	}
	if !listed {
		t.Fatalf("session %s not present in list output:\n%s", runResp.SessionID, listOut)
	}

	describeOut := runProcess(t, workspace, env, telosBin, "describe", runResp.SessionID, "--json")
	var describeResp struct {
		SessionID        string   `json:"session_id"`
		Status           string   `json:"status"`
		CompletionReason *string  `json:"completion_reason"`
		TotalCostUSD     *float64 `json:"total_cost_usd"`
	}
	mustJSON(t, describeOut, &describeResp)
	if describeResp.SessionID != runResp.SessionID || describeResp.Status != "completed" {
		t.Fatalf("unexpected describe response: %+v", describeResp)
	}
	if describeResp.CompletionReason == nil || *describeResp.CompletionReason != "verifier_conceded" {
		t.Fatalf("completion reason: got %#v", describeResp.CompletionReason)
	}
	if describeResp.TotalCostUSD == nil || *describeResp.TotalCostUSD != 0.03 {
		t.Fatalf("total cost: got %#v", describeResp.TotalCostUSD)
	}

	logsOut := runProcess(t, workspace, env, telosBin, "logs", "--verbose", runResp.SessionID)
	if !strings.Contains(logsOut, "fake prover wrote hello.txt") || !strings.Contains(logsOut, "fake verifier accepted") {
		t.Fatalf("verbose logs missing fake turns:\n%s", logsOut)
	}

	archivePath := filepath.Join(runResp.SessionDir, "specs", "fake-cli", "workspace.tar.gz")
	got := readTarGzFile(t, archivePath, "./hello.txt")
	if got != "fake executor wrote this\n" {
		t.Fatalf("workspace archive hello.txt: got %q", got)
	}

	stopOut := runProcess(t, workspace, env, telosBin, "stop", runResp.SessionID, "--json")
	var stopResp struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	mustJSON(t, stopOut, &stopResp)
	if stopResp.SessionID != runResp.SessionID || stopResp.Status != "completed" {
		t.Fatalf("unexpected stop response: %+v", stopResp)
	}
}

func writeFakeScenario(t *testing.T, dir string) {
	t.Helper()
	scenarioDir := filepath.Join(dir, ".telos-testfake")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scenario := `{
  "turns": [
    {
      "role": "prover",
      "logs": "fake prover wrote hello.txt\n\n<progress_update>fake write complete</progress_update>\n",
      "writes": {"hello.txt": "fake executor wrote this\n"},
      "cost_usd": 0.01,
      "input_tokens": 10,
      "output_tokens": 5,
      "model": "test-fake"
    },
    {
      "role": "verifier",
      "status": "CONCEDE",
      "logs": "fake verifier accepted\n\n<status>CONCEDE</status>\n",
      "cost_usd": 0.02,
      "input_tokens": 8,
      "output_tokens": 4,
      "model": "test-fake"
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(scenarioDir, "agent.json"), []byte(scenario), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeIntegrationSpec(t *testing.T, dir string) {
	t.Helper()
	spec := "---\nversion: 0.1.0\nname: fake-cli\nplatform: local\n---\n# Goal\n\nCreate hello.txt.\n"
	if err := os.WriteFile(filepath.Join(dir, "SPEC.md"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initFakeIntegrationGitRepo(t *testing.T, dir string) {
	t.Helper()
	runProcess(t, dir, nil, "git", "init", "-q")
	runProcess(t, dir, nil, "git", "add", "-A")
	runProcess(t, dir, nil, "git", "-c", "user.name=Telos", "-c", "user.email=telos@local", "commit", "-q", "-m", "init")
}

func waitForFakeSessionStatus(t *testing.T, dir string, env []string, telosBin string, sessionID string, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := runProcess(t, dir, env, telosBin, "describe", sessionID, "--json")
		var resp struct {
			Status string `json:"status"`
		}
		mustJSON(t, out, &resp)
		if resp.Status == want {
			return
		}
		if resp.Status == "failed" || resp.Status == "stopped" {
			t.Fatalf("session reached %s while waiting for %s:\n%s", resp.Status, want, out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach %s", sessionID, want)
}

func runProcess(t *testing.T, dir string, extraEnv []string, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = fakeIntegrationEnv(extraEnv)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("%s %s timed out\n%s", name, strings.Join(args, " "), out)
	}
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func fakeIntegrationEnv(extra []string) []string {
	drop := map[string]bool{
		"TELOS_API_TOKEN":         true,
		"TELOS_API_ENDPOINT":      true,
		"TELOS_PARENT_SESSION_ID": true,
		"TELOS_RUNTIME":           true,
		"TELOS_SESSION_DIR":       true,
		"TELOS_SESSION_ID":        true,
		"TELOSD_PATH":             true,
	}
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !drop[key] {
			env = append(env, item)
		}
	}
	return append(env, extra...)
}

func mustJSON(t *testing.T, text string, dest any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), dest); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, text)
	}
}

func readTarGzFile(t *testing.T, archivePath string, name string) string {
	t.Helper()
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err != nil {
			t.Fatalf("%s not found in %s", name, archivePath)
		}
		if header.Name != name {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}
