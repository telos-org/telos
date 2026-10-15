package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

const testPlanSpec = "---\nname: demo\nversion: 1.2.3\nplatform: cloud\nskills: []\n---\n\nServe a demo.\n"

func TestCLIPlanSubprocess(t *testing.T) {
	if raw := os.Getenv("TELOS_TEST_PLAN_COMMAND"); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			panic(err)
		}
		cmdPlan(args)
		os.Exit(0)
	}
}

func TestCLIPlanRequiresMessageBeforeAnyNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid message made API call: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, []byte(testPlanSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{specPath, "--out=" + filepath.Join(t.TempDir(), "new.plan")},
		{specPath, "--out=" + filepath.Join(t.TempDir(), "new.plan"), "-m", "  "},
		{specPath, "--message", "two\nlines"},
	} {
		command := exec.Command(os.Args[0], "-test.run=^TestCLIPlanSubprocess$")
		encoded, _ := json.Marshal(args)
		command.Env = append(os.Environ(), "TELOS_TEST_PLAN_COMMAND="+string(encoded))
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "--message") {
			t.Fatalf("args=%v err=%v output=%s", args, err, output)
		}
	}
}

func TestPlanPermissionAndCapabilityFailurePrecedeUploads(t *testing.T) {
	for _, mode := range []string{"preview", "saved"} {
		for _, tt := range []struct {
			name, capabilities, access, want string
		}{
			{"old Cloud", `{}`, `{"can_plan":true}`, "does not support deployment plans"},
			{"planning denied", `{"deployment_plans":true}`, `{"can_plan":false,"can_apply":true}`, "do not have permission to plan"},
		} {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/capabilities":
						_, _ = w.Write([]byte(tt.capabilities))
					case "/api/deployment-plans/access":
						_, _ = w.Write([]byte(tt.access))
					default:
						t.Errorf("permission failure made a downstream call: %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				_, err := createCloudPlan(cloud.NewClient(server.URL, "token"), cloudPlanInput{specArg: "not-needed.md", mode: mode, revisionMessage: "Update the demo"})
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err=%v", err)
				}
			})
		}
	}
}

func TestPlanUpdatesIgnoreCreationDefaultsAndRejectExplicitOverrides(t *testing.T) {
	var submissions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/deployments/sess_123":
			_, _ = w.Write([]byte(`{"id":"sess_123","current_revision_id":"rev_7"}`))
		case "POST /api/deployment-plans":
			submissions.Add(1)
			var options cloud.DeploymentPlanOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil || options.Create != nil || options.Update == nil || options.Mode != "preview" {
				t.Errorf("update changed creation settings: %+v err=%v", options, err)
			}
			_ = json.NewEncoder(w).Encode(testDeploymentPlan("preview", "preview"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_MODEL", "telos/default")
	t.Setenv("TELOS_THINKING", "invalid-creation-default")
	path := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(path, []byte(testPlanSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{nil, {"--model", ""}, {"--thinking", "high"}} {
		args := append([]string{path, "--goal", "sess_123", "--json"}, flags...)
		encoded, _ := json.Marshal(args)
		process := exec.Command(os.Args[0], "-test.run=^TestCLIPlanSubprocess$")
		process.Env = append(os.Environ(), "TELOS_TEST_PLAN_COMMAND="+string(encoded))
		output, err := process.CombinedOutput()
		if (err != nil) != (flags != nil) || (flags != nil && !strings.Contains(string(output), "can only configure a new")) {
			t.Fatalf("args=%v err=%v output=%s", args, err, output)
		}
	}
	if submissions.Load() != 1 {
		t.Fatalf("submissions=%d", submissions.Load())
	}
}

func TestPlanMessageValidationBeforeAnyNetwork(t *testing.T) {
	for _, value := range []string{"", " \u2003\u00a0 ", "two\nlines", "bad\x00text", "split\u2028line", "split\u2029paragraph", strings.Repeat("🚀", 201)} {
		for _, mode := range []string{"saved"} {
			t.Run(mode+"/"+value, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.NotFound(w, r) }))
				defer server.Close()
				_, err := createCloudPlan(cloud.NewClient(server.URL, "token"), cloudPlanInput{specArg: "SPEC.md", mode: mode, revisionMessage: value})
				if err == nil || !strings.Contains(err.Error(), "--message") || calls.Load() != 0 {
					t.Fatalf("err=%v calls=%d", err, calls.Load())
				}
			})
		}
	}
	message := strings.Repeat("🚀", 200)
	if got, err := normalizePlanMessage("  "+message+"  ", true); err != nil || got != message {
		t.Fatalf("valid Unicode message rejected: %q %v", got, err)
	}
	if got, err := normalizePlanMessage("", false); err != nil || got != "" {
		t.Fatalf("preview requires a message: %q %v", got, err)
	}
}

func testDeploymentPlan(mode, status string) cloud.ChangeRequestRecord {
	base := "rev_7"
	legacyValid := true
	return cloud.ChangeRequestRecord{
		Kind: "change_request", UpdateNumber: 1, PreparedPlanID: "cp_original", LegacyConfirmationValid: &legacyValid,
		ID: "cr_saved", DeploymentID: "sess_123", Mode: mode, Status: status, Action: "update",
		BaseRevisionID: &base, CanConfirm: true, CanDiscard: true,
		ReviewURL: "https://example.com/changes/cr_saved",
		Preview:   &cloud.DeploymentPlanPreview{BaseSpec: testPlanSpec, ProposedSpec: strings.Replace(testPlanSpec, "Serve a demo.", "Serve an updated demo.", 1)},
	}
}

func serveDeploymentPlanPrerequisites(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost && r.URL.Path == "/api/deployment-plans/packages" {
		_, _ = w.Write([]byte(`{"scope":"personal","name":"plan-artifact","version":"1.0.0","ref":"@personal/plan-artifact:1.0.0","digest":"sha256:private"}`))
		return true
	}
	return serveDeploymentPlanReadPrerequisites(w, r)
}

func serveDeploymentPlanReadPrerequisites(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/api/deployments":
		_, _ = w.Write([]byte(`{"deployments":[]}`))
	case "/api/capabilities":
		_, _ = w.Write([]byte(`{"deployment_plans":true,"deployment_change_requests":true}`))
	case "/api/deployment-plans/access":
		_, _ = w.Write([]byte(`{"can_plan":true,"can_apply":true}`))
	case "/api/account/bootstrap":
		_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[]}`))
	default:
		return false
	}
	return true
}

func TestCancellationDuringUploadCannotSubmitSavedPlan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var submissions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/deployment-plans/packages" {
			cancel()
		}
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.URL.Path == "/api/deployment-plans" {
			submissions.Add(1)
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "SPEC.md")
	_ = os.WriteFile(path, []byte(testPlanSpec), 0o600)
	_, err := createCloudPlan(cloud.NewClient(server.URL, "token").WithContext(ctx), cloudPlanInput{specArg: path, mode: "saved", revisionMessage: "Update the demo"})
	if !errors.Is(err, context.Canceled) || submissions.Load() != 0 {
		t.Fatalf("canceled upload submitted saved plan: err=%v submissions=%d", err, submissions.Load())
	}
}

func TestMemberCanSavePrivatePlanAndBookmarkWithoutDeploying(t *testing.T) {
	var submissions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/deployment-plans/access" {
			_, _ = w.Write([]byte(`{"can_plan":true,"can_apply":false}`))
			return
		}
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/deployment-plans":
			submissions++
			var options cloud.DeploymentPlanOptions
			_ = json.NewDecoder(r.Body).Decode(&options)
			if options.Mode != "saved" || options.Create == nil || options.Create.PackageRef != "@personal/plan-artifact:1.0.0" || options.Create.RevisionMessage != "Create the demo" {
				t.Errorf("wrong saved proposal: %+v", options)
			}
			request := testDeploymentPlan("saved", "awaiting_confirmation")
			request.Action = "create"
			request.BaseRevisionID = nil
			_ = json.NewEncoder(w).Encode(request)
		default:
			t.Errorf("plan attempted unexpected API: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, []byte(testPlanSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "anything.extension")
	out := captureStdout(t, func() {
		cmdPlan([]string{specPath, "--out=" + path, "--json", "-m", "  Create the demo  "})
	})
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &receipt); err != nil || string(receipt["operation"]) != `"requested"` {
		t.Fatalf("invalid agent receipt: %s err=%v", out, err)
	}
	bookmark, err := readSavedDeploymentPlan(path)
	if err != nil || bookmark == nil || bookmark.OrgID != "org_personal" || bookmark.APIEndpoint != server.URL || bookmark.ChangeRequestID != "cr_saved" {
		t.Fatalf("bookmark=%+v err=%v", bookmark, err)
	}
	if err := runCloudPlan(cloudPlanInput{specArg: specPath, mode: "saved"}, path, true); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("saved file overwritten: %v", err)
	}
	if submissions != 1 {
		t.Fatalf("overwrite created another remote request: %d", submissions)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "token") || strings.Contains(string(data), "Serve a demo") {
		t.Fatalf("bookmark contains credentials or payload: %s", data)
	}
}

func TestSavedPlanParserRejectsInvalidJSONAndUnsupportedVersion(t *testing.T) {
	for _, tt := range []struct{ contents, want string }{
		{`{bad`, "invalid saved plan file"},
		{`{"version":2}`, "one plan or request ID"},
		{`{"version":1}`, "one plan or request ID"},
		{`{"version":99}`, "unsupported saved plan format 99"},
		{`[]`, "invalid saved plan file"},
		{`{"version":1} {}`, "invalid saved plan file"},
		{strings.Repeat(" ", 64*1024) + `{}`, "too large"},
	} {
		path := filepath.Join(t.TempDir(), "SPEC.md")
		_ = os.WriteFile(path, []byte(tt.contents), 0o600)
		if _, err := readSavedDeploymentPlan(path); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("bookmark=%q error=%v want=%q", tt.contents, err, tt.want)
		}
	}
	path := filepath.Join(t.TempDir(), "change.plan")
	_ = os.WriteFile(path, []byte(testPlanSpec), 0o600)
	bookmark, err := readSavedDeploymentPlan(path)
	if err != nil || bookmark != nil {
		t.Fatalf("file classification used its extension: %+v %v", bookmark, err)
	}
}

func TestSavedPlanWriteNeverReplacesConcurrentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "change.plan")
	writer, err := prepareSavedPlanWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.close()
	_ = os.WriteFile(path, []byte("existing"), 0o600)
	if err := writer.save(savedDeploymentPlan{Version: 1}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("save should refuse existing path: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "existing" {
		t.Fatalf("overwrote existing file: %s", data)
	}
}

func TestPlanOutputPreservesCreationSettingsAndSnapshotBypass(t *testing.T) {
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.Action = "create"
	model, thinking := "resolved-provider/model", "high"
	request.Creation = &cloud.DeploymentPlanCreation{
		Name: "books", AgentModel: &model, AgentThinking: &thinking,
		Inference: &cloud.InferenceSelection{Source: "subscription", ConnectionID: "conn_42", Model: "model"},
		SecretIDs: []string{"secret_42"},
	}
	request.Force = true
	var output bytes.Buffer
	control := cloud.NewClient("https://api.example.com", "token")
	printDeploymentPlan(&output, control, &request)
	for _, want := range []string{"books", model, thinking, "conn_42", "secret_42", "--force", "restore point"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("review omitted %q: %s", want, output.String())
		}
	}
	jsonOutput := captureStdout(t, func() { printDeploymentPlanJSON(control, &request, "") })
	var receipt struct {
		Request cloud.ChangeRequestRecord `json:"change_request"`
	}
	if err := json.Unmarshal([]byte(jsonOutput), &receipt); err != nil || receipt.Request.Creation == nil || receipt.Request.Creation.Inference.ConnectionID != "conn_42" || !receipt.Request.Force {
		t.Fatalf("JSON lost frozen settings: %s err=%v", jsonOutput, err)
	}
}
