package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
)

func TestFreshYesConfirmsOnlyItsPreparedPlan(t *testing.T) {
	for _, kind := range []string{"plan", "change_request"} {
		t.Run(kind, func(t *testing.T) {
			request := testDeploymentPlan("apply", "awaiting_confirmation")
			request.Kind, request.UpdateNumber, request.PreparedPlanID = kind, 1, "cp_exact"
			if kind == "plan" {
				request.ID = "plan_direct"
			}
			submissions, confirmations := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveDeploymentPlanPrerequisites(w, r) {
					return
				}
				switch r.URL.Path {
				case "/api/deployment-plans":
					submissions++
					var body map[string]json.RawMessage
					_ = json.NewDecoder(r.Body).Decode(&body)
					if _, exists := body["auto_confirm"]; exists {
						t.Error("sent authorization before a plan existed")
					}
				case "/api/deployment-plans/plan_direct/apply", "/api/deployments/sess_123/change-requests/cr_saved/confirm":
					confirmations++
					var body map[string]json.RawMessage
					_ = json.NewDecoder(r.Body).Decode(&body)
					if string(body["expected_plan_id"]) != `"cp_exact"` || string(body["expected_current_revision_id"]) != `"rev_7"` {
						t.Errorf("confirmation lost identity: %v", body)
					}
					request.Status = "applied"
				default:
					t.Errorf("unexpected API %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(request)
			}))
			defer server.Close()
			configureCloudTest(t, server.URL)
			specPath := filepath.Join(t.TempDir(), "SPEC.md")
			_ = os.WriteFile(specPath, []byte(testPlanSpec), 0o600)
			captureStdout(t, func() {
				if err := runCloudApply(cloudPlanInput{specArg: specPath, mode: "apply", yes: true, revisionMessage: "Update"}, true); err != nil {
					t.Fatal(err)
				}
			})
			if submissions != 1 || confirmations != 1 {
				t.Fatalf("submissions=%d confirmations=%d", submissions, confirmations)
			}
		})
	}
}

func TestYesDoesNotApproveAReplacementAfterAConfirmationRace(t *testing.T) {
	request := testDeploymentPlan("apply", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID = 1, "cp_reviewed"
	confirms := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			confirms++
			http.Error(w, "plan replaced", http.StatusConflict)
			return
		}
		current := request
		current.UpdateNumber, current.PreparedPlanID = 2, "cp_unreviewed"
		_ = json.NewEncoder(w).Encode(current)
	}))
	defer server.Close()
	_, err := awaitCloudApply(context.Background(), cloud.NewClient(server.URL, "token"), &request, true,
		strings.NewReader(""), io.Discard, io.Discard, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "changed") || confirms != 1 {
		t.Fatalf("replacement approved: err=%v confirmations=%d", err, confirms)
	}
}

func TestExactSavedReferenceCanApplyFormerRegularRequest(t *testing.T) {
	request := testDeploymentPlan("apply", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID = 2, "cp_second"
	confirms := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			confirms++
			request.Status = "applied"
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	bookmark := newSavedDeploymentPlan(cloud.NewClient(server.URL, "token"), &request, "org_personal")
	captureStdout(t, func() {
		if err := runSavedCloudApply(&bookmark, "", true); err != nil {
			t.Fatal(err)
		}
	})
	if confirms != 1 {
		t.Fatalf("confirmations=%d", confirms)
	}
}
