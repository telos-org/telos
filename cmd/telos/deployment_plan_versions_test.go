package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestCloudPlanOutputUsesPreparedVersionAndPackage(t *testing.T) {
	const preparedRef = "@personal/plan-artifact:0.0.0-plan.sha256-prepared"
	const preparedDigest = "sha256:prepared"
	preparedSpec := strings.Replace(testPlanSpec, "version: 1.2.3", "version: 1.2.4", 1)
	preparedSpec = strings.Replace(preparedSpec, "Serve a demo.", "Serve an updated demo.", 1)
	for _, tt := range []struct {
		name, mode, kind string
		queued           bool
	}{
		{name: "preview", mode: "preview", kind: "plan"},
		{name: "saved request", mode: "saved", kind: "change_request"},
		{name: "direct apply", mode: "apply", kind: "plan"},
		{name: "protected apply", mode: "apply", kind: "change_request"},
		{name: "queued apply", mode: "apply", kind: "change_request", queued: true},
	} {
		for _, format := range []string{"terminal", "json"} {
			t.Run(tt.name+"/"+format, func(t *testing.T) {
				prepared := testDeploymentPlan(tt.mode, "awaiting_confirmation")
				prepared.Kind = tt.kind
				prepared.PackageRef, prepared.PackageDigest = preparedRef, preparedDigest
				prepared.Preview.ProposedSpec = preparedSpec
				if tt.kind == "plan" {
					prepared.ID = "plan_prepared"
				}
				if tt.mode == "apply" {
					prepared.Status = "applied"
				}
				var uploads, submissions, confirms int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/deployment-plans/packages" {
						uploads++
					}
					if serveDeploymentPlanPrerequisites(w, r) {
						return
					}
					switch r.Method + " " + r.URL.Path {
					case "GET /api/deployments/sess_123":
						_, _ = w.Write([]byte(`{"id":"sess_123","package_ref":"@personal/demo:1.2.3","current_revision_id":"rev_7"}`))
					case "POST /api/deployment-plans":
						submissions++
						var options cloud.DeploymentPlanOptions
						if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
							t.Error(err)
						}
						if options.Mode != tt.mode || options.Update == nil || options.Update.PackageRef != "@personal/plan-artifact:1.0.0" {
							t.Errorf("unexpected source proposal: %+v", options)
						}
						if tt.queued {
							waiting := prepared
							waiting.Status, waiting.Preview = "queued", nil
							waiting.PackageRef, waiting.PackageDigest = "@personal/plan-artifact:1.0.0", "sha256:private"
							_ = json.NewEncoder(w).Encode(waiting)
							return
						}
						_ = json.NewEncoder(w).Encode(prepared)
					case "GET /api/deployments/sess_123/change-requests/cr_saved":
						_ = json.NewEncoder(w).Encode(prepared)
					case "POST /api/deployments/sess_123/change-requests/cr_saved/confirm":
						confirms++
						applied := prepared
						applied.Status = "applied"
						_ = json.NewEncoder(w).Encode(applied)
					default:
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				configureCloudTest(t, server.URL)
				source := filepath.Join(t.TempDir(), "SPEC.md")
				if err := os.WriteFile(source, []byte(testPlanSpec), 0o600); err != nil {
					t.Fatal(err)
				}
				input := cloudPlanInput{specArg: source, sessionID: "sess_123", mode: tt.mode, revisionMessage: "Update the demo", autoConfirm: tt.mode == "apply"}
				var bookmarkPath string
				if tt.mode == "saved" {
					bookmarkPath = filepath.Join(t.TempDir(), "change.plan")
				}
				jsonOut := format == "json"
				var runErr error
				output := captureStdout(t, func() {
					if tt.mode == "apply" {
						runErr = runCloudApply(input, jsonOut)
					} else {
						runErr = runCloudPlan(input, bookmarkPath, jsonOut)
					}
				})
				if runErr != nil {
					t.Fatal(runErr)
				}
				if jsonOut {
					assertPreparedPackageReceipt(t, output, tt.kind, preparedRef, preparedDigest, preparedSpec)
				} else if !strings.Contains(output, "1.2.3 -> 1.2.4") || !strings.Contains(output, "+version: 1.2.4") {
					t.Fatalf("terminal did not show Cloud's prepared version: %s", output)
				}
				if actual, err := os.ReadFile(source); err != nil || string(actual) != testPlanSpec {
					t.Fatalf("Cloud preparation changed local SPEC.md: %q err=%v", actual, err)
				}
				if tt.mode == "saved" {
					// A saved reference uses the exact Cloud proposal, even after
					// the local source is edited or removed.
					if err := os.Remove(source); err != nil {
						t.Fatal(err)
					}
					bookmark, err := readSavedDeploymentPlan(bookmarkPath)
					if err != nil {
						t.Fatal(err)
					}
					output = captureStdout(t, func() { runErr = runSavedCloudApply(bookmark, "", true) })
					if runErr != nil || confirms != 1 {
						t.Fatalf("saved apply err=%v confirms=%d", runErr, confirms)
					}
					assertPreparedPackageReceipt(t, output, tt.kind, preparedRef, preparedDigest, preparedSpec)
				}
				if uploads != 1 || submissions != 1 {
					t.Fatalf("unexpected artifact upload or replan: uploads=%d submissions=%d", uploads, submissions)
				}
			})
		}
	}
}

func assertPreparedPackageReceipt(t *testing.T, output, kind, ref, digest, proposedSpec string) {
	t.Helper()
	var receipt struct {
		Package       cloud.PackageVersionRecord `json:"package"`
		Plan          *cloud.ChangeRequestRecord `json:"plan"`
		ChangeRequest *cloud.ChangeRequestRecord `json:"change_request"`
	}
	if err := json.Unmarshal([]byte(output), &receipt); err != nil {
		t.Fatal(err)
	}
	proposal := receipt.ChangeRequest
	if kind == "plan" {
		proposal = receipt.Plan
		if receipt.ChangeRequest != nil {
			t.Fatalf("direct plan receipt contains a Change Request: %s", output)
		}
	} else if receipt.Plan != nil {
		t.Fatalf("Change Request receipt contains a direct plan: %s", output)
	}
	if proposal == nil || proposal.PackageRef != ref || proposal.PackageDigest != digest || proposal.Preview == nil || proposal.Preview.ProposedSpec != proposedSpec {
		t.Fatalf("receipt did not preserve Cloud's prepared proposal: %s", output)
	}
	if receipt.Package.Ref != ref || receipt.Package.Digest != digest || receipt.Package.Scope != "personal" || receipt.Package.Name != "plan-artifact" || receipt.Package.Version != "0.0.0-plan.sha256-prepared" {
		t.Fatalf("receipt reported the original upload instead of the prepared package: %s", output)
	}
}
