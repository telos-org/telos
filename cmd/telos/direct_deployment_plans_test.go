package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func testDirectDeploymentPlan(mode, status string) cloud.ChangeRequestRecord {
	plan := testDeploymentPlan(mode, status)
	plan.Kind = "plan"
	plan.ID = "plan_saved"
	plan.ReviewURL = "https://preview.example.com/plans/plan_saved?org=org_personal"
	return plan
}

func TestDirectSavedPlanWritesPlanReferenceWithoutCreatingChangeRequest(t *testing.T) {
	var submissions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/deployment-plans" {
			t.Errorf("saving applied or used a Change Request route: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		submissions.Add(1)
		var options cloud.DeploymentPlanOptions
		if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
			t.Error(err)
		}
		if options.Mode != "saved" || options.AutoConfirm || options.Create == nil || options.Create.RevisionMessage != "Create the demo" {
			t.Errorf("unexpected saved proposal: %+v", options)
		}
		plan := testDirectDeploymentPlan("saved", "awaiting_confirmation")
		plan.Action, plan.BaseRevisionID = "create", nil
		_ = json.NewEncoder(w).Encode(plan)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, []byte(testPlanSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "deployment.plan")
	var planErr error
	output := captureStdout(t, func() {
		planErr = runCloudPlan(cloudPlanInput{specArg: specPath, mode: "saved", revisionMessage: "Create the demo"}, path, true)
	})
	if planErr != nil {
		t.Fatal(planErr)
	}
	bookmark, err := readSavedDeploymentPlan(path)
	if err != nil || bookmark == nil || bookmark.PlanID != "plan_saved" || bookmark.ChangeRequestID != "" || bookmark.OrgID != "org_personal" {
		t.Fatalf("invalid direct bookmark: %+v err=%v", bookmark, err)
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &receipt); err != nil {
		t.Fatal(err)
	}
	if submissions.Load() != 1 || receipt["plan"] == nil || receipt["change_request"] != nil || string(receipt["operation"]) != `"planned"` || string(receipt["plan_file"]) != `"`+path+`"` {
		t.Fatalf("saved direct plan was reported as a Change Request or applied: %s", output)
	}
	encoded, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(encoded), "change_request_id") || strings.Contains(string(encoded), "control-token") {
		t.Fatalf("bookmark contains request ID or credentials: %s err=%v", encoded, err)
	}
	bookmark.ChangeRequestID = "cr_old"
	encoded, _ = json.Marshal(bookmark)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSavedDeploymentPlan(path); err == nil {
		t.Fatal("ambiguous bookmark containing both reference kinds was accepted")
	}
}

func TestDirectSavedApplyResumesOnceAndDoesNotReplan(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		for _, initialStatus := range []string{"awaiting_confirmation", "applying", "applied"} {
			t.Run(action+"/"+initialStatus, func(t *testing.T) {
				var applies atomic.Int32
				var applied atomic.Bool
				applied.Store(initialStatus == "applied")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/deployment-plans/access" {
						wantSession := "sess_123"
						if action == "create" {
							wantSession = ""
						}
						if r.URL.Query().Get("deployment_id") != wantSession {
							t.Errorf("wrong access target for %s: %s", action, r.URL)
						}
						_, _ = w.Write([]byte(`{"can_plan":true,"can_apply":true,"requires_change_requests":false}`))
						return
					}
					if serveDeploymentPlanPrerequisites(w, r) {
						return
					}
					plan := testDirectDeploymentPlan("saved", initialStatus)
					plan.Action = action
					if action == "create" {
						plan.BaseRevisionID = nil
					}
					switch r.Method + " " + r.URL.Path {
					case "GET /api/deployment-plans/plan_saved":
					case "POST /api/deployment-plans/plan_saved/apply":
						var body map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						wantBase := `"rev_7"`
						if action == "create" {
							wantBase = "null"
						}
						if string(body["expected_current_revision_id"]) != wantBase {
							t.Errorf("lost frozen baseline: %v", body)
						}
						applies.Add(1)
						applied.Store(true)
					default:
						t.Errorf("saved apply replanned, uploaded, or used a request route: %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
						return
					}
					if applied.Load() {
						plan.Status = "applied"
					}
					_ = json.NewEncoder(w).Encode(plan)
				}))
				defer server.Close()
				configureCloudTest(t, server.URL)
				bookmark := &savedDeploymentPlan{Version: 1, PlanID: "plan_saved", DeploymentID: "sess_123", Context: "personal", OrgID: "org_personal", APIEndpoint: server.URL}
				for attempt := 0; attempt < 2; attempt++ {
					var applyErr error
					output := captureStdout(t, func() { applyErr = runSavedCloudApply(bookmark, "", true) })
					var receipt map[string]json.RawMessage
					if err := json.Unmarshal([]byte(output), &receipt); err != nil || applyErr != nil || string(receipt["operation"]) != `"applied"` || receipt["plan"] == nil || receipt["change_request"] != nil {
						t.Fatalf("retry %d: err=%v output=%s parse=%v", attempt, applyErr, output, err)
					}
					if string(receipt["deployment_url"]) != `"https://preview.example.com/goals/sess_123?org=org_personal"` {
						t.Errorf("missing scoped live deployment link: %s", output)
					}
				}
				wantApplies := int32(1)
				if initialStatus == "applied" {
					wantApplies = 0
				}
				if applies.Load() != wantApplies {
					t.Fatalf("apply calls=%d, want %d", applies.Load(), wantApplies)
				}
			})
		}
	}
}

func TestDirectInteractiveApplyUsesApplyAndDiscardRoutes(t *testing.T) {
	for _, answer := range []string{"yes\n", "no\n", "yes", ""} {
		t.Run(answer, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				path, status := "/api/deployment-plans/plan_saved/discard", "discarded"
				if answer == "yes\n" {
					path, status = "/api/deployment-plans/plan_saved/apply", "applied"
				}
				if r.Method != http.MethodPost || r.URL.Path != path {
					t.Errorf("unexpected interactive action: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(testDirectDeploymentPlan("apply", status))
			}))
			defer server.Close()
			initial := testDirectDeploymentPlan("apply", "awaiting_confirmation")
			var prompt bytes.Buffer
			plan, err := awaitCloudApply(context.Background(), cloud.NewClient(server.URL, "token"), &initial, false,
				strings.NewReader(answer), io.Discard, &prompt, time.Hour)
			if calls.Load() != 1 || !strings.Contains(prompt.String(), "Type yes to confirm") {
				t.Fatalf("calls=%d prompt=%s", calls.Load(), prompt.String())
			}
			if answer == "yes\n" {
				if err != nil || plan.Status != "applied" {
					t.Fatalf("confirmation failed: %+v %v", plan, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "discarded") {
				t.Fatalf("non-confirmation applied the plan: %+v %v", plan, err)
			}
		})
	}
}

func TestDirectApplySnapshotFailureStopsInsteadOfPolling(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "interactive", true: "yes"}[automatic], func(t *testing.T) {
			var calls atomic.Int32
			initial := testDirectDeploymentPlan("apply", "awaiting_confirmation")
			blocked := initial
			reason, code := "The current revision must finish snapshotting before it can change.", "snapshot_pending"
			blocked.Error, blocked.ErrorCode = &reason, &code
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if automatic || r.Method != http.MethodPost || r.URL.Path != "/api/deployment-plans/plan_saved/apply" {
					t.Errorf("snapshot error triggered polling or another apply: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(blocked)
			}))
			defer server.Close()
			if automatic {
				initial = blocked
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var prompt bytes.Buffer
			_, err := awaitCloudApply(ctx, cloud.NewClient(server.URL, "token"), &initial, automatic,
				strings.NewReader("yes\n"), io.Discard, &prompt, time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), reason) || !strings.Contains(err.Error(), initial.ReviewURL) || ctx.Err() != nil {
				t.Fatalf("snapshot failure was not returned immediately with a dashboard link: %v", err)
			}
			wantCalls := int32(1)
			if automatic {
				wantCalls = 0
				if prompt.Len() != 0 {
					t.Fatalf("--yes prompted: %s", prompt.String())
				}
			}
			if calls.Load() != wantCalls {
				t.Fatalf("calls=%d want=%d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestDirectPreviewOutputDoesNotDescribeAChangeRequest(t *testing.T) {
	plan := testDirectDeploymentPlan("preview", "awaiting_confirmation")
	control := cloud.NewClient("https://api.example.com", "token")
	var output bytes.Buffer
	printDeploymentPreview(&output, control, &cloudPlanResult{request: &plan, specName: "demo", currentRef: "@personal/demo:1.2.3"})
	for _, unwanted := range []string{"Change Request", "Request ", "Status ", "awaiting confirmation"} {
		if strings.Contains(output.String(), unwanted) {
			t.Errorf("preview contains %q: %s", unwanted, output.String())
		}
	}
	for _, wanted := range []string{"Spec", "demo", "Preview", plan.ReviewURL, "Serve an updated demo", "Preview only"} {
		if !strings.Contains(output.String(), wanted) {
			t.Errorf("preview missing %q: %s", wanted, output.String())
		}
	}
	jsonOutput := captureStdout(t, func() { printDeploymentPlanJSON(control, &plan, nil, "") })
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOutput), &receipt); err != nil || string(receipt["operation"]) != `"preview"` || receipt["plan"] == nil || receipt["change_request"] != nil || receipt["preview_url"] == nil || receipt["deployment_url"] != nil {
		t.Fatalf("invalid direct preview receipt: %s parse=%v", jsonOutput, err)
	}
}

func TestDirectCLIYesSnapshotFailureExitsWithDashboardLink(t *testing.T) {
	var submissions atomic.Int32
	reason, code := "The current revision must finish snapshotting before it can change.", "snapshot_pending"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/deployments/sess_123":
			_, _ = w.Write([]byte(`{"id":"sess_123","package_ref":"@personal/demo:1.2.2","current_revision_id":"rev_7"}`))
		case "POST /api/deployment-plans":
			submissions.Add(1)
			var options cloud.DeploymentPlanOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
			}
			if options.Mode != "apply" || !options.AutoConfirm || options.Update == nil || options.Update.ExpectedCurrentRevisionID != "rev_7" {
				t.Errorf("invalid automatic apply submission: %+v", options)
			}
			plan := testDirectDeploymentPlan("apply", "awaiting_confirmation")
			plan.Error, plan.ErrorCode = &reason, &code
			_ = json.NewEncoder(w).Encode(plan)
		default:
			t.Errorf("automatic apply polled or retried after a snapshot error: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, []byte(testPlanSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIPlanApplySubprocess$")
	args, _ := json.Marshal([]string{"apply", specPath, "--session", "sess_123", "--message", "Update the demo", "--yes", "--json"})
	command.Env = append(os.Environ(), "TELOS_TEST_PLAN_COMMAND="+string(args))
	output, err := command.CombinedOutput()
	if err == nil || ctx.Err() != nil || submissions.Load() != 1 || !strings.Contains(string(output), reason) || !strings.Contains(string(output), "/plans/plan_saved") {
		t.Fatalf("automatic CLI apply did not fail promptly with its plan link: err=%v ctx=%v submissions=%d output=%s", err, ctx.Err(), submissions.Load(), output)
	}
	if strings.Contains(string(output), `"operation"`) {
		t.Fatalf("snapshot failure printed a success receipt: %s", output)
	}
}

func TestDirectApplyLostResponseObservesExistingPlan(t *testing.T) {
	var applies, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/deployment-plans/plan_saved/apply":
			applies.Add(1)
			http.Error(w, `{"detail":"acknowledgement lost"}`, http.StatusBadGateway)
		case "GET /api/deployment-plans/plan_saved":
			reads.Add(1)
			_ = json.NewEncoder(w).Encode(testDirectDeploymentPlan("saved", "applied"))
		default:
			t.Errorf("lost-response recovery changed its target: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	initial := testDirectDeploymentPlan("saved", "awaiting_confirmation")
	plan, err := confirmCloudRequest(cloud.NewClient(server.URL, "token"), &initial)
	if err != nil || plan.Status != "applied" || applies.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("lost-response recovery failed: plan=%+v err=%v applies=%d reads=%d", plan, err, applies.Load(), reads.Load())
	}
}
