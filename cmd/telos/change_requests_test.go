package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestPendingGoalDescriptionPreservesCloudReason(t *testing.T) {
	for _, tt := range []struct{ status, reason, failure string }{
		{"pending", "Waiting for confirmation.", ""},
		{"pending", "Preparing the plan.", ""},
		{"working", "The request is confirmed and waiting to start.", ""},
		{"needs_attention", "The requesting account is no longer available.", ""},
		{"stopped", "The initial request was discarded.", ""},
		{"needs_attention", "Request failed.", "Unable to start the Goal."},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			session := cloud.SessionRecord{State: "pending", Status: tt.status, StatusReason: tt.reason}
			want := tt.reason
			if tt.failure != "" {
				session.FailureReason, want = &tt.failure, tt.failure
			}
			var out bytes.Buffer
			printCloudSessionDescription(&out, session)
			if got := configOutputValue(t, out.String(), "Reason"); got != want {
				t.Fatalf("reason=%q want=%q", got, want)
			}
			if got := configOutputValue(t, out.String(), "Revision"); got != "not deployed yet" {
				t.Fatalf("pending Goal reported a deployed revision: %s", out.String())
			}
		})
	}
}

func TestDescribeKeepsPendingRequestsSeparateFromCurrentStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/capabilities":
			_, _ = w.Write([]byte(`{"deployment_change_requests":true}`))
		case "/api/deployments/sess_123/change-requests":
			_, _ = w.Write([]byte(`{"requests":[{"id":"req_old","status":"applied"},{"id":"req_42","deployment_id":"sess_123","action":"update","status":"awaiting_confirmation","review_url":"https://app.example.com/review"},{"id":"req_43","deployment_id":"sess_123","action":"restore","status":"queued"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)
	session := &cloud.SessionRecord{ID: "sess_123", Status: "ready", CurrentRevisionID: "rev_7", PackageDigest: "sha256:old"}
	out := captureStdout(t, func() { describeCloudGoal(session, "personal", "", "", true) })
	var result struct {
		Status                string                      `json:"status"`
		PackageDigest         string                      `json:"package_digest"`
		PendingChangeRequests []cloud.ChangeRequestRecord `json:"pending_change_requests"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || result.PackageDigest != "sha256:old" || len(result.PendingChangeRequests) != 2 || result.PendingChangeRequests[0].ID != "req_42" {
		t.Fatalf("description = %s", out)
	}
	out = captureStdout(t, func() { describeCloudGoal(session, "personal", "", "", false) })
	if !strings.Contains(out, "Status") || !strings.Contains(out, "Change requests") || !strings.Contains(out, "awaiting confirmation") || !strings.Contains(out, "https://app.example.com/review") {
		t.Fatalf("description = %s", out)
	}
}

func TestChangeRequestURLKeepsOrganizationAndServerProvidedOrigin(t *testing.T) {
	control := cloud.NewClient(cloud.DefaultAPIEndpoint, "token")
	control.OrgID = "org_acme"
	request := cloud.ChangeRequestRecord{ID: "req_42", DeploymentID: "sess_123"}
	want := "https://usetelos.ai/deployments/sess_123?org=org_acme&request=req_42&tab=change-requests"
	if got := cloudRequestReviewURL(control, request); got != want {
		t.Fatalf("URL=%s want=%s", got, want)
	}
	request.ReviewURL = "https://custom.example.com/changes/42"
	if got := cloudRequestReviewURL(control, request); got != request.ReviewURL {
		t.Fatalf("server-provided URL ignored: %s", got)
	}
}
