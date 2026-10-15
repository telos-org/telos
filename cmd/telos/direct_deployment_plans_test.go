package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func testDirectDeploymentPlan(mode, status string) cloud.ChangeRequestRecord {
	plan := testDeploymentPlan(mode, status)
	plan.Kind = "plan"
	plan.ID = "plan_saved"
	plan.UpdateNumber, plan.PreparedPlanID = 0, ""
	plan.ReviewURL = "https://preview.example.com/plans/plan_saved?org=org_personal"
	return plan
}

func TestUnprotectedPlanOutSavesOptionalChangeRequest(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		t.Run(action, func(t *testing.T) {
			var submissions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/deployment-plans/access" {
					_ = json.NewEncoder(w).Encode(map[string]bool{
						"can_plan": true, "can_apply": action == "create", "requires_change_requests": false,
					})
					return
				}
				if serveDeploymentPlanPrerequisites(w, r) {
					return
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /api/deployments/sess_123":
					_, _ = w.Write([]byte(`{"id":"sess_123","package_ref":"@personal/demo:1.2.3","current_revision_id":"rev_7"}`))
				case "POST /api/deployment-plans":
					submissions.Add(1)
					var options cloud.DeploymentPlanOptions
					if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
						t.Error(err)
					}
					if options.Mode != "saved" {
						t.Errorf("optional request can deploy automatically: %+v", options)
					}
					request := testDeploymentPlan("saved", "awaiting_confirmation")
					request.Kind, request.Action = "change_request", action
					if action == "create" {
						request.BaseRevisionID = nil
						if options.Create == nil || options.Update != nil {
							t.Errorf("wrong creation proposal: %+v", options)
						}
					} else if options.Update == nil || options.Update.ExpectedCurrentRevisionID != "rev_7" || options.Create != nil {
						t.Errorf("wrong update proposal: %+v", options)
					}
					_ = json.NewEncoder(w).Encode(request)
				default:
					t.Errorf("saving requested execution or unexpected API: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			configureCloudTest(t, server.URL)
			specPath := filepath.Join(t.TempDir(), "SPEC.md")
			if err := os.WriteFile(specPath, []byte(testPlanSpec), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "optional.plan")
			input := cloudPlanInput{specArg: specPath, mode: "saved", revisionMessage: "Update the demo"}
			if action == "update" {
				input.sessionID = "sess_123"
			}
			var planErr error
			output := captureStdout(t, func() { planErr = runCloudPlan(input, path, true) })
			if planErr != nil {
				t.Fatal(planErr)
			}
			bookmark, err := readSavedDeploymentPlan(path)
			if err != nil || bookmark == nil || bookmark.ChangeRequestID != "cr_saved" || bookmark.PlanID != "" {
				t.Fatalf("optional request lost its saved reference: %+v err=%v", bookmark, err)
			}
			var receipt map[string]json.RawMessage
			if err := json.Unmarshal([]byte(output), &receipt); err != nil || submissions.Load() != 1 || string(receipt["operation"]) != `"requested"` || receipt["change_request"] == nil || receipt["review_url"] == nil || receipt["plan"] != nil {
				t.Fatalf("wrong optional request receipt: %s err=%v", output, err)
			}
		})
	}
}

func TestLegacyDirectSavedPlanWritesPlanReference(t *testing.T) {
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
		if options.Mode != "saved" || options.Create == nil || options.Create.RevisionMessage != "Create the demo" {
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

func TestDirectPreviewOutputDoesNotDescribeAChangeRequest(t *testing.T) {
	plan := testDirectDeploymentPlan("preview", "awaiting_confirmation")
	control := cloud.NewClient("https://api.example.com", "token")
	var output bytes.Buffer
	printDeploymentPreview(&output, control, &cloudPlanResult{request: &plan, specName: "demo", currentRef: "@personal/demo:1.2.3"})
	for _, unwanted := range []string{"\nRequest ", "\nStatus ", "awaiting confirmation"} {
		if strings.Contains(output.String(), unwanted) {
			t.Errorf("preview contains %q: %s", unwanted, output.String())
		}
	}
	for _, wanted := range []string{"Spec", "demo", "Preview", plan.ReviewURL, "Serve an updated demo", "Preview only", "--out=FILE with --message to save a Change Request"} {
		if !strings.Contains(output.String(), wanted) {
			t.Errorf("preview missing %q: %s", wanted, output.String())
		}
	}
	jsonOutput := captureStdout(t, func() { printDeploymentPlanJSON(control, &plan, "") })
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOutput), &receipt); err != nil || string(receipt["operation"]) != `"preview"` || receipt["plan"] == nil || receipt["change_request"] != nil || receipt["preview_url"] == nil || receipt["deployment_url"] != nil {
		t.Fatalf("invalid direct preview receipt: %s parse=%v", jsonOutput, err)
	}
}
