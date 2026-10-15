package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
				} else {
					prepared.UpdateNumber, prepared.PreparedPlanID = 1, "cp_prepared"
				}
				var uploads, submissions, confirms int
				confirmPath := "/api/deployments/sess_123/change-requests/cr_saved/confirm"
				if tt.kind == "plan" {
					confirmPath = "/api/deployment-plans/plan_prepared/apply"
				}
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
						var options struct {
							Mode        string          `json:"mode"`
							AutoConfirm json.RawMessage `json:"auto_confirm"`
							Update      struct {
								PackageRef string `json:"package_ref"`
							} `json:"update"`
						}
						if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
							t.Error(err)
						}
						if options.Mode != tt.mode || options.AutoConfirm != nil || options.Update.PackageRef != "@personal/plan-artifact:1.0.0" {
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
					case "POST " + confirmPath:
						confirms++
						var body map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if string(body["expected_current_revision_id"]) != `"rev_7"` ||
							(tt.kind != "plan" && string(body["expected_plan_id"]) != `"cp_prepared"`) {
							t.Errorf("confirmation lost the reviewed plan: %s", body)
						}
						applied := prepared
						applied.Status = "applied"
						if tt.kind != "plan" {
							applied.Preview = nil // Confirmation receipts omit the preview.
						}
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
				input := cloudPlanInput{specArg: source, sessionID: "sess_123", mode: tt.mode, revisionMessage: "Update the demo", yes: tt.mode == "apply"}
				var bookmarkPath string
				if tt.mode == "saved" {
					bookmarkPath = filepath.Join(t.TempDir(), "change.plan")
				}
				jsonOut := format == "json"
				wantSpec := preparedSpec
				if tt.mode == "apply" && tt.kind != "plan" {
					wantSpec = ""
				}
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
					assertPreparedPackageReceipt(t, output, tt.kind, preparedRef, preparedDigest, wantSpec)
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
					assertPreparedPackageReceipt(t, output, tt.kind, preparedRef, preparedDigest, "")
				}
				wantConfirms := 1
				if tt.mode == "preview" {
					wantConfirms = 0
				}
				if uploads != 1 || submissions != 1 || confirms != wantConfirms {
					t.Fatalf("uploads=%d submissions=%d confirmations=%d (want %d)", uploads, submissions, confirms, wantConfirms)
				}
			})
		}
	}
}

func assertPreparedPackageReceipt(t *testing.T, output, kind, ref, digest, proposedSpec string) {
	t.Helper()
	type proposalReceipt struct {
		PackageRef    string `json:"package_ref"`
		PackageDigest string `json:"package_digest"`
		Preview       *struct {
			ProposedSpec string `json:"proposed_spec"`
		} `json:"preview"`
	}
	var receipt struct {
		Package struct {
			Ref     string `json:"ref"`
			Digest  string `json:"digest"`
			Scope   string `json:"scope"`
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"package"`
		Plan          *proposalReceipt `json:"plan"`
		ChangeRequest *proposalReceipt `json:"change_request"`
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
	if proposal == nil || proposal.PackageRef != ref || proposal.PackageDigest != digest {
		t.Fatalf("receipt did not preserve Cloud's prepared proposal: %s", output)
	}
	if proposedSpec == "" && proposal.Preview != nil || proposedSpec != "" && (proposal.Preview == nil || proposal.Preview.ProposedSpec != proposedSpec) {
		t.Fatalf("receipt did not preserve the server's optional preview: %s", output)
	}
	if receipt.Package.Ref != ref || receipt.Package.Digest != digest || receipt.Package.Scope != "personal" || receipt.Package.Name != "plan-artifact" || receipt.Package.Version != "0.0.0-plan.sha256-prepared" {
		t.Fatalf("receipt reported the original upload instead of the prepared package: %s", output)
	}
}
