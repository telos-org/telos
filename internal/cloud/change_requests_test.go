package cloud

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionMutationsNegotiateChangeRequestReceipts(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		t.Run(action, func(t *testing.T) {
			var payload map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/capabilities" {
					_, _ = w.Write([]byte(`{"deployment_change_requests":true}`))
					return
				}
				if r.Header.Get("X-Telos-Change-Requests") != "1" || r.Header.Get("X-Telos-Org-Id") != "org_acme" {
					t.Errorf("missing negotiation or org header: %v", r.Header)
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"outcome":        "requested",
					"deployment":     map[string]any{"id": "sess_123", "current_revision_id": "rev_7", "status": "ready"},
					"change_request": map[string]any{"id": "req_42", "deployment_id": "sess_123", "action": action, "status": "queued", "require_confirmation": true, "review_url": "https://app.example.com/review"},
				})
			}))
			defer srv.Close()
			client := NewClient(srv.URL, "test-token")
			client.OrgID = "org_acme"
			var result *SessionMutationResult
			var err error
			if action == "create" {
				requireConfirmation := true
				result, err = client.CreateSession(SessionCreateOptions{Name: "books", PackageRef: "@acme/books:1.2.3", RequireConfirmation: &requireConfirmation})
				if payload["require_confirmation"] != true {
					t.Fatalf("create policy not sent: %#v", payload)
				}
			} else {
				result, err = client.UpdateSession("sess_123", SessionUpdateOptions{PackageRef: "@acme/books:1.2.3", ExpectedCurrentRevisionID: "rev_7"})
				if payload["expected_current_revision_id"] != "rev_7" {
					t.Fatalf("baseline not sent: %#v", payload)
				}
				if _, exists := payload["require_confirmation"]; exists {
					t.Fatalf("update tried to change policy: %#v", payload)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != "requested" || result.Deployment.CurrentRevisionID != "rev_7" || result.ChangeRequest.ID != "req_42" || result.ChangeRequest.ReviewURL != "https://app.example.com/review" {
				t.Fatalf("receipt = %+v", result)
			}
		})
	}
}

func TestCreateConfirmationRequiresSupportedCloud(t *testing.T) {
	for _, requireConfirmation := range []bool{false, true} {
		for _, capabilityStatus := range []int{http.StatusOK, http.StatusNotFound, http.StatusServiceUnavailable} {
			mutations := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/capabilities" {
					mutations++
				}
				w.WriteHeader(capabilityStatus)
				_, _ = w.Write([]byte(`{}`))
			}))
			_, err := NewClient(srv.URL, "token").CreateSession(SessionCreateOptions{RequireConfirmation: &requireConfirmation})
			srv.Close()
			if err == nil || mutations != 0 {
				t.Fatalf("unsupported policy was accepted: status=%d required=%v mutations=%d err=%v", capabilityStatus, requireConfirmation, mutations, err)
			}
		}
	}
}

func TestAcceptedMutationMustIdentifyItsChangeRequest(t *testing.T) {
	for _, body := range []string{
		`{"id":"sess_123","status":"ready"}`,
		`{"outcome":"requested","deployment":{"id":"sess_123"}}`,
		`{"outcome":"requested","deployment":{"id":"sess_123"},"change_request":{"id":"req_42","deployment_id":"sess_other","status":"queued"}}`,
		`{"outcome":"requested","deployment":{"id":"sess_123"},"change_request":{"id":"req_42","deployment_id":"sess_123"}}`,
	} {
		_, err := readSessionMutationResponse(&http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(body))})
		if err == nil {
			t.Fatalf("accepted malformed receipt: %s", body)
		}
	}
}
