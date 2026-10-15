package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
)

const testPlanSpec = "---\nname: demo\nversion: 1.2.3\nplatform: cloud\nskills: []\n---\n\nServe a demo.\n"

func TestPlanMessageValidationBeforeAnyNetwork(t *testing.T) {
	for _, value := range []string{"", " \u2003\u00a0 ", "two\nlines", "bad\x00text", "split\u2028line", "split\u2029paragraph", strings.Repeat("🚀", 201)} {
		for _, mode := range []string{"saved", "apply"} {
			t.Run(mode+"/"+value, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.NotFound(w, r) }))
				defer server.Close()
				_, err := createCloudPlan(cloud.NewClient(server.URL, "token"), cloudPlanInput{specArg: "SPEC.md", mode: mode, yes: mode == "apply", revisionMessage: value})
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

func TestCLIRequiresMessageAndRejectsSavedOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid message made API call: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	_ = os.WriteFile(specPath, []byte(testPlanSpec), 0o600)
	savedPath := filepath.Join(t.TempDir(), "change.plan")
	bookmark := savedDeploymentPlan{Version: savedPlanVersion, UpdateNumber: 1, PreparedPlanID: "cp_original", ChangeRequestID: "cr_saved", DeploymentID: "sess_123", Context: "personal", OrgID: "org_personal", APIEndpoint: server.URL}
	encoded, _ := json.Marshal(bookmark)
	_ = os.WriteFile(savedPath, encoded, 0o600)
	for _, args := range [][]string{
		{"plan", specPath, "--out=" + filepath.Join(t.TempDir(), "new.plan")},
		{"apply", specPath, "--yes", "--json"},
		{"apply", "@acme/books:1.0.0", "--yes", "-m", "  "},
		{"apply", savedPath, "--message", "Replace the saved message"},
		{"apply", savedPath, "-m", ""},
	} {
		command := exec.Command(os.Args[0], "-test.run=^TestCLIPlanApplySubprocess$")
		encoded, _ := json.Marshal(args)
		command.Env = append(os.Environ(), "TELOS_TEST_PLAN_COMMAND="+string(encoded))
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "--message") {
			t.Fatalf("args=%v err=%v output=%s", args, err, output)
		}
	}
}

func testDeploymentPlan(mode, status string) cloud.ChangeRequestRecord {
	base := "rev_7"
	return cloud.ChangeRequestRecord{
		ID: "cr_saved", DeploymentID: "sess_123", Mode: mode, Status: status, Action: "update",
		BaseRevisionID: &base, CanConfirm: true, CanDiscard: true,
		ReviewURL: "https://example.com/changes/cr_saved",
		Preview:   &cloud.DeploymentPlanPreview{BaseSpec: testPlanSpec, ProposedSpec: strings.Replace(testPlanSpec, "Serve a demo.", "Serve an updated demo.", 1)},
	}
}

// Shared wire fixtures for command tests. Individual tests handle the mutation
// endpoint themselves so assertions inspect actual client payloads.
func serveDeploymentPlanPrerequisites(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/deployments":
		_, _ = w.Write([]byte(`{"deployments":[]}`))
	case "/api/capabilities":
		_, _ = w.Write([]byte(`{"deployment_plans":true,"deployment_change_requests":true}`))
	case "/api/deployment-plans/access":
		_, _ = w.Write([]byte(`{"can_plan":true,"can_apply":true}`))
	case "/api/account/bootstrap":
		_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[]}`))
	case "/api/deployment-plans/packages":
		_, _ = w.Write([]byte(`{"scope":"personal","name":"plan-artifact","version":"1.0.0","ref":"@personal/plan-artifact:1.0.0","digest":"sha256:private"}`))
	default:
		return false
	}
	return true
}

func TestFreshApplyRequiresExplicitNoninteractiveConfirmation(t *testing.T) {
	for _, tt := range []struct {
		name                           string
		yes, json, stdinTTY, promptTTY bool
		wantErr                        bool
	}{
		{name: "interactive", stdinTTY: true, promptTTY: true},
		{name: "stdin pipe", promptTTY: true, wantErr: true},
		{name: "hidden prompt", stdinTTY: true, wantErr: true},
		{name: "json terminal", json: true, stdinTTY: true, promptTTY: true, wantErr: true},
		{name: "agent", yes: true, json: true},
		{name: "yes redirected", yes: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFreshApplyConfirmation(tt.yes, tt.json, tt.stdinTTY, tt.promptTTY)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%t", err, tt.wantErr)
			}
		})
	}
	var networkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkCalls.Add(1)
		http.Error(w, "must not call", http.StatusInternalServerError)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	err := runCloudApply(cloudPlanInput{specArg: "SPEC.md", mode: "apply"}, true)
	if err == nil || !strings.Contains(err.Error(), "--yes") || networkCalls.Load() != 0 {
		t.Fatalf("err=%v networkCalls=%d", err, networkCalls.Load())
	}
}

func TestPlanPermissionAndCapabilityFailurePrecedeUploads(t *testing.T) {
	for _, tt := range []struct {
		name, capabilities, access, want string
	}{
		{"old Cloud", `{}`, `{"can_plan":true,"can_apply":true}`, "does not support deployment plans"},
		{"member cannot apply", `{"deployment_plans":true}`, `{"can_plan":true,"can_apply":false}`, "--out=change.plan"},
		{"protected proposer cannot apply", `{"deployment_plans":true}`, `{"can_plan":true,"can_apply":false,"requires_change_requests":true}`, "--out=change.plan"},
		{"unprotected plan-only member", `{"deployment_plans":true}`, `{"can_plan":true,"can_apply":false,"requires_change_requests":false}`, "Change Request for an authorized editor"},
	} {
		t.Run(tt.name, func(t *testing.T) {
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
			_, err := createCloudPlan(cloud.NewClient(server.URL, "token"), cloudPlanInput{specArg: "not-needed.md", mode: "apply", revisionMessage: "Update the demo"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCancellationDuringUploadCannotSubmitAutoConfirmedApply(t *testing.T) {
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
	_, err := createCloudPlan(cloud.NewClient(server.URL, "token").WithContext(ctx), cloudPlanInput{specArg: path, mode: "apply", yes: true, revisionMessage: "Update the demo"})
	if !errors.Is(err, context.Canceled) || submissions.Load() != 0 {
		t.Fatalf("canceled upload submitted apply: err=%v submissions=%d", err, submissions.Load())
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

func TestSavedPlanRejectsForeignEndpointContextAndAccount(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodGet && r.URL.Path == "/api/account/bootstrap" {
			_, _ = w.Write([]byte(`{"personal_org_id":"org_current"}`))
			return
		}
		t.Errorf("foreign bookmark accessed %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()
	client := cloud.NewClient(server.URL, "token")
	for _, bookmark := range []savedDeploymentPlan{
		{APIEndpoint: "https://attacker.example", Context: "personal"},
		{APIEndpoint: server.URL, Context: "@another-org"},
	} {
		if err := validateSavedDeploymentPlan(client, &bookmark); err == nil {
			t.Fatal("foreign bookmark accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("foreign endpoint or context caused %d API calls", calls)
	}
	bookmark := savedDeploymentPlan{APIEndpoint: server.URL, Context: "personal", OrgID: "org_previous"}
	if err := validateSavedDeploymentPlan(client, &bookmark); err == nil || !strings.Contains(err.Error(), "different organization") {
		t.Fatalf("foreign account accepted: %v", err)
	}
	if calls != 1 {
		t.Fatalf("account mismatch made %d calls; only account lookup is allowed", calls)
	}
}

func TestQueuedApplyConfirmsFreshHeadPreview(t *testing.T) {
	initial := testDeploymentPlan("apply", "queued")
	initial.Preview = nil
	initial.BaseRevisionID = nil
	var confirmCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := testDeploymentPlan("apply", "awaiting_confirmation")
		revision := "rev_8"
		request.BaseRevisionID = &revision
		request.Preview.BaseSpec = "---\nname: demo\nversion: 1.2.3\n---\nAlice's changes.\n"
		if strings.HasSuffix(r.URL.Path, "/confirm") {
			confirmCount++
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["expected_current_revision_id"] != "rev_8" {
				t.Errorf("confirmed outdated baseline: %v", payload)
			}
			request.Status = "applied"
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	var preview bytes.Buffer
	request, err := awaitCloudApply(context.Background(), cloud.NewClient(server.URL, "token"), &initial, false,
		strings.NewReader("yes\n"), &preview, time.Millisecond)
	if err != nil || request.Status != "applied" || confirmCount != 1 || !strings.Contains(preview.String(), "Alice's changes") || !strings.Contains(preview.String(), "Type yes") {
		t.Fatalf("request=%+v err=%v confirms=%d preview=%s", request, err, confirmCount, preview.String())
	}
}

func TestDashboardConfirmationReleasesWaitingTerminal(t *testing.T) {
	initial := testDeploymentPlan("apply", "awaiting_confirmation")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("terminal repeated dashboard action: %s %s", r.Method, r.URL.Path)
		}
		request := testDeploymentPlan("apply", "applied")
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := awaitCloudApply(ctx, cloud.NewClient(server.URL, "token"), &initial, false,
		reader, io.Discard, time.Millisecond)
	if err != nil || request.Status != "applied" {
		t.Fatalf("dashboard confirmation did not finish terminal: %+v %v", request, err)
	}
}

func TestApplyMissingSnapshotLeavesRequestPending(t *testing.T) {
	for _, tt := range []struct {
		name                        string
		direct, interactive, queued bool
		path                        string
	}{
		{name: "request initially blocked"},
		{name: "direct initially blocked", direct: true},
		{name: "request confirmation blocked", interactive: true, path: "/api/deployments/sess_123/change-requests/cr_saved/confirm"},
		{name: "direct confirmation blocked", direct: true, interactive: true, path: "/api/deployment-plans/plan_saved/apply"},
		{name: "queued request blocked", queued: true, path: "/api/deployments/sess_123/change-requests/cr_saved"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			initial := testDeploymentPlan("apply", "awaiting_confirmation")
			if tt.direct {
				initial = testDirectDeploymentPlan("apply", "awaiting_confirmation")
			}
			blocked := initial
			reason, code := "The current revision must finish snapshotting before it can change.", "snapshot_pending"
			blocked.Error, blocked.ErrorCode = &reason, &code
			if tt.queued {
				initial.Status, initial.Preview = "queued", nil
			} else if !tt.interactive {
				initial = blocked
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method := http.MethodPost
				if tt.queued {
					method = http.MethodGet
				}
				if calls.Add(1) > 1 || tt.path == "" || r.Method != method || r.URL.Path != tt.path {
					t.Errorf("unexpected snapshot action: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(blocked)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var output bytes.Buffer
			request, err := awaitCloudApply(ctx, cloud.NewClient(server.URL, "token"), &initial, !tt.interactive,
				strings.NewReader("yes\n"), &output, time.Millisecond)
			if request != nil || err == nil || ctx.Err() != nil {
				t.Fatalf("did not stop for a snapshot choice: request=%+v err=%v ctx=%v", request, err, ctx.Err())
			}
			for _, want := range []string{"still pending", reason, "Confirm again", "Apply Now", blocked.ReviewURL} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q from snapshot guidance: %v", want, err)
				}
			}
			wantCalls := int32(0)
			if tt.path != "" {
				wantCalls = 1
			}
			if calls.Load() != wantCalls || strings.Contains(output.String(), "Type yes") != tt.interactive {
				t.Fatalf("calls=%d want=%d output=%s", calls.Load(), wantCalls, output.String())
			}
		})
	}
}

func TestSavedApplyMissingSnapshotDoesNotReportApproval(t *testing.T) {
	var confirmations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		request := testDeploymentPlan("saved", "awaiting_confirmation")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/deployments/sess_123/change-requests/cr_saved":
		case "POST /api/deployments/sess_123/change-requests/cr_saved/confirm":
			confirmations.Add(1)
			reason, code := "No snapshot is available.", "snapshot_pending"
			request.Error, request.ErrorCode = &reason, &code
		default:
			t.Errorf("unexpected saved snapshot action: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	bookmark := &savedDeploymentPlan{Version: 1, ChangeRequestID: "cr_saved", DeploymentID: "sess_123", Context: "personal", OrgID: "org_personal", APIEndpoint: server.URL}
	var applyErr error
	output := captureStdout(t, func() { applyErr = runSavedCloudApply(bookmark, "", true) })
	if applyErr == nil || !strings.Contains(applyErr.Error(), "still pending") || !strings.Contains(applyErr.Error(), "https://example.com/changes/cr_saved") || output != "" || confirmations.Load() != 1 {
		t.Fatalf("saved snapshot failure reported approval: err=%v output=%s confirms=%d", applyErr, output, confirmations.Load())
	}
}

func TestSnapshotChoiceAfterLostConfirmationResponse(t *testing.T) {
	var confirmations, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			confirmations.Add(1)
			http.Error(w, `{"detail":"acknowledgement lost"}`, http.StatusBadGateway)
			return
		}
		reads.Add(1)
		request := testDeploymentPlan("saved", "awaiting_confirmation")
		reason, code := "No snapshot is available.", "snapshot_pending"
		request.Error, request.ErrorCode = &reason, &code
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	initial := testDeploymentPlan("saved", "awaiting_confirmation")
	request, err := confirmCloudRequest(cloud.NewClient(server.URL, "token"), &initial)
	if request != nil || err == nil || !strings.Contains(err.Error(), "still pending") || !strings.Contains(err.Error(), initial.ReviewURL) || confirmations.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("lost response hid snapshot choice: request=%+v err=%v confirms=%d reads=%d", request, err, confirmations.Load(), reads.Load())
	}
}

func TestInteractiveNoEOFAndCancellationLeaveRemoteRequestUntouched(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		canceled    bool
	}{
		{name: "no", input: "no\n"}, {name: "EOF"}, {name: "partial yes then EOF", input: "yes"}, {name: "interrupt queued", canceled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mutationCount int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mutationCount++
				t.Errorf("stopping the terminal touched the remote request: %s %s", r.Method, r.URL.Path)
				http.NotFound(w, r)
			}))
			defer server.Close()
			initial := testDeploymentPlan("apply", "awaiting_confirmation")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.canceled {
				initial.Status = "queued"
				initial.Preview = nil
				cancel()
			}
			_, err := awaitCloudApply(ctx, cloud.NewClient(server.URL, "token"), &initial, false,
				strings.NewReader(tt.input), io.Discard, time.Second)
			if err == nil || !strings.Contains(err.Error(), "stopped waiting") || mutationCount != 0 {
				t.Fatalf("err=%v mutations=%d", err, mutationCount)
			}
		})
	}
}

func TestCancellationInterruptsInFlightPlanRequests(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method {
					t.Errorf("unexpected request during cancellation: %s", r.Method)
				}
				close(started)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			initial := testDeploymentPlan("apply", "queued")
			if method == http.MethodPost {
				initial.Status = "awaiting_confirmation"
			}
			result := make(chan error, 1)
			go func() {
				_, err := awaitCloudApply(ctx, cloud.NewClient(server.URL, "token"), &initial, true,
					strings.NewReader(""), io.Discard, time.Millisecond)
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not start")
			}
			cancel()
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), "stopped waiting") || !strings.Contains(err.Error(), initial.ReviewURL) {
					t.Fatalf("cancellation lost recovery guidance: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Ctrl-C did not interrupt the in-flight request")
			}
		})
	}
}

func TestConfirmationLostResponseReadsExistingResult(t *testing.T) {
	for _, tt := range []struct {
		name, path, action string
		status             int
	}{
		{"request", "/api/deployments/sess_123/change-requests/cr_saved", "/confirm", http.StatusConflict},
		{"direct", "/api/deployment-plans/plan_saved", "/apply", http.StatusBadGateway},
	} {
		t.Run(tt.name, func(t *testing.T) {
			initial := testDeploymentPlan("saved", "awaiting_confirmation")
			if tt.name == "direct" {
				initial = testDirectDeploymentPlan("saved", "awaiting_confirmation")
			}
			confirms, reads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "POST " + tt.path + tt.action:
					confirms++
					http.Error(w, "acknowledgement lost", tt.status)
				case "GET " + tt.path:
					reads++
					applied := initial
					applied.Status = "applied"
					_ = json.NewEncoder(w).Encode(applied)
				default:
					t.Errorf("lost-response recovery changed its target: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			request, err := confirmCloudRequest(cloud.NewClient(server.URL, "token"), &initial)
			if err != nil || request.Status != "applied" || confirms != 1 || reads != 1 {
				t.Fatalf("request=%+v err=%v confirms=%d reads=%d", request, err, confirms, reads)
			}
		})
	}
}

func TestSavedAndInteractiveApplyReportInlineExecutionFailure(t *testing.T) {
	for _, status := range []string{"failed", "discarded"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveDeploymentPlanPrerequisites(w, r) {
					return
				}
				request := testDeploymentPlan("saved", "awaiting_confirmation")
				if r.Method == http.MethodPost {
					request.Status = status
					reason := "execution stopped"
					request.Error = &reason
				}
				_ = json.NewEncoder(w).Encode(request)
			}))
			defer server.Close()
			configureCloudTest(t, server.URL)
			bookmark := &savedDeploymentPlan{Version: 1, ChangeRequestID: "cr_saved", DeploymentID: "sess_123", Context: "personal", OrgID: "org_personal", APIEndpoint: server.URL}
			var applyErr error
			output := captureStdout(t, func() { applyErr = runSavedCloudApply(bookmark, "", true) })
			if applyErr == nil || !strings.Contains(applyErr.Error(), status) || output != "" {
				t.Fatalf("saved apply reported success: err=%v output=%s", applyErr, output)
			}
			initial := testDeploymentPlan("apply", "awaiting_confirmation")
			_, err := awaitCloudApply(context.Background(), cloud.NewClient(server.URL, "token"), &initial, false,
				strings.NewReader("yes\n"), io.Discard, time.Second)
			if err == nil || !strings.Contains(err.Error(), "execution stopped") {
				t.Fatalf("interactive apply reported success: %v", err)
			}
		})
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
	request.Status = "confirmed"
	reason := "Waiting for the current revision's snapshot."
	request.Error = &reason
	output.Reset()
	printDeploymentPlanResult(&output, control, &request)
	if !strings.Contains(output.String(), reason) || !strings.Contains(output.String(), "--context personal") {
		t.Fatalf("confirmed result hides pending work or selected context: %s", output.String())
	}
}

func TestSavedApplyUsesImmutableRequestWithoutReplanningOrPrompting(t *testing.T) {
	var confirms int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.URL.Path != "/api/deployments/sess_123/change-requests/cr_saved" && r.URL.Path != "/api/deployments/sess_123/change-requests/cr_saved/confirm" {
			t.Errorf("saved apply replanned or uploaded: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		request := testDeploymentPlan("saved", "awaiting_confirmation")
		if r.Method == http.MethodPost {
			confirms++
			request.Status = "applied"
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	path := filepath.Join(t.TempDir(), "no-special-extension")
	bookmark := savedDeploymentPlan{Version: 1, ChangeRequestID: "cr_saved", DeploymentID: "sess_123", Context: "personal", OrgID: "org_personal", APIEndpoint: server.URL}
	data, _ := json.Marshal(bookmark)
	_ = os.WriteFile(path, data, 0o600)
	out := captureStdout(t, func() { cmdApply([]string{path, "--json"}) })
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &receipt); err != nil || string(receipt["operation"]) != `"applied"` || confirms != 1 {
		t.Fatalf("out=%s err=%v confirms=%d", out, err, confirms)
	}
}

func TestCLIPlanApplySubprocess(t *testing.T) {
	if raw := os.Getenv("TELOS_TEST_PLAN_COMMAND"); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			panic(err)
		}
		if args[0] == "plan" {
			cmdPlan(args[1:])
		} else {
			cmdApply(args[1:])
		}
		os.Exit(0)
	}
}

func TestCLIRequiresConfirmationBeforeAnyNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid invocation made API call: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	_ = os.WriteFile(specPath, []byte(testPlanSpec), 0o600)
	for _, args := range [][]string{
		{"apply", specPath, "--message", "Update the demo"}, {"apply", specPath, "--json", "--message", "Update the demo"},
	} {
		command := exec.Command(os.Args[0], "-test.run=^TestCLIPlanApplySubprocess$")
		encoded, _ := json.Marshal(args)
		command.Env = append(os.Environ(), "TELOS_TEST_PLAN_COMMAND="+string(encoded))
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "interactive confirmation is unavailable") {
			t.Fatalf("args=%v err=%v output=%s", args, err, output)
		}
	}
}
