package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func TestApplyCloudSessionPackageReturnsRequestWithoutPolling(t *testing.T) {
	var getCalls, putCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/capabilities":
			_, _ = w.Write([]byte(`{"deployment_change_requests":true}`))
		case "/api/deployments/sess_123":
			if r.Method == http.MethodGet {
				getCalls++
				_, _ = w.Write([]byte(`{"id":"sess_123","package_ref":"@acme/books:1.0.0","current_revision_id":"rev_7","status":"ready"}`))
				return
			}
			putCalls++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["expected_current_revision_id"] != "rev_7" {
				t.Errorf("missing expected revision: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"outcome":"requested","deployment":{"id":"sess_123","name":"books","current_revision_id":"rev_7","status":"ready"},"change_request":{"id":"req_42","deployment_id":"sess_123","action":"update","status":"awaiting_confirmation","package_digest":"sha256:new","require_confirmation":true}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	operation, result, err := applyCloudSessionPackage(cloud.NewClient(srv.URL, "token"), "books", "@acme/books:2.0.0", "sess_123", sessionRuntimeConfig{}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if operation != "requested" || result.ChangeRequest.ID != "req_42" || putCalls != 1 || getCalls != 1 {
		t.Fatalf("operation=%s result=%+v get=%d put=%d", operation, result, getCalls, putCalls)
	}
	var out bytes.Buffer
	printCloudChangeRequestReceipt(&out, result, "@acme", "https://app.example.com/review")
	for _, want := range []string{"requested books", "Status    awaiting confirmation", "Current   rev_7", "Proposed  sha256:new", "Review    https://app.example.com/review"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("receipt missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "ready") || strings.Contains(out.String(), "Logs") {
		t.Fatalf("pending receipt implies new revision is active: %s", out.String())
	}
}

func TestApplyCloudSessionPackageReportsInlineResultWithoutPolling(t *testing.T) {
	for _, status := range []string{"applying", "applied"} {
		t.Run(status, func(t *testing.T) {
			var getCalls, putCalls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/capabilities":
					_, _ = w.Write([]byte(`{"deployment_change_requests":true}`))
				case "/api/deployments/sess_123":
					if r.Method == http.MethodGet {
						getCalls++
						_, _ = w.Write([]byte(`{"id":"sess_123","package_ref":"@acme/books:1.0.0","current_revision_id":"rev_7","status":"ready"}`))
						return
					}
					putCalls++
					w.WriteHeader(http.StatusAccepted)
					revisionID := "rev_8"
					_ = json.NewEncoder(w).Encode(cloud.SessionMutationResult{
						Outcome: "requested",
						Deployment: &cloud.SessionRecord{
							ID: "sess_123", Name: "books", CurrentRevisionID: revisionID, Status: "working",
						},
						ChangeRequest: &cloud.ChangeRequestRecord{
							ID: "req_42", DeploymentID: "sess_123", Action: "update", Status: status,
							PackageDigest: "sha256:new", ResultRevisionID: &revisionID,
						},
					})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			operation, result, err := applyCloudSessionPackage(cloud.NewClient(srv.URL, "token"), "books", "@acme/books:2.0.0", "sess_123", sessionRuntimeConfig{}, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if operation != "requested" || result.Deployment.CurrentRevisionID != "rev_8" || result.ChangeRequest.Status != status || putCalls != 1 || getCalls != 1 {
				t.Fatalf("operation=%s result=%+v get=%d put=%d", operation, result, getCalls, putCalls)
			}
			var out bytes.Buffer
			printCloudChangeRequestReceipt(&out, result, "@acme", "https://app.example.com/review")
			for _, want := range []string{"requested books", "Status    " + status, "Current   rev_8", "Proposed  sha256:new"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("receipt missing %q: %s", want, out.String())
				}
			}
			for _, unwanted := range []string{"unchanged", "rev_7", "ready"} {
				if strings.Contains(out.String(), unwanted) {
					t.Fatalf("receipt misrepresents inline execution: %s", out.String())
				}
			}
		})
	}
}

func TestPlanDisplaysConfirmationPolicy(t *testing.T) {
	pkg := testApplyPackage(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/capabilities":
			_, _ = w.Write([]byte(`{"deployment_change_requests":true}`))
		case "/api/deployments/sess_123":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess_123", "package_ref": "@telos/demo:1.2.3", "package_digest": pkg.Digest})
		case "/api/packages/telos/demo/versions/1.2.3/bundle":
			_, _ = w.Write(pkg.Bytes)
		case "/api/deployments/sess_123/change-policy":
			_, _ = w.Write([]byte(`{"require_confirmation":true,"can_manage":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	comparison, err := compareCloudSessionSpecWithState(cloud.NewClient(srv.URL, "token"), "sess_123", []byte("# Updated"), planSpecState{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printPlanPreview(&out, &spec.CompiledEnvironment{Environment: &spec.EnvironmentSpec{Name: "demo"}}, "SPEC.md", "cloud", "@acme", comparison)
	if !strings.Contains(out.String(), "Confirm   required in the dashboard") {
		t.Fatalf("missing confirmation policy: %s", out.String())
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
	out := captureStdout(t, func() { describeCloudSession(session, "personal", true) })
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
	out = captureStdout(t, func() { describeCloudSession(session, "personal", false) })
	if !strings.Contains(out, "Status    ready") || !strings.Contains(out, "Change requests") || !strings.Contains(out, "awaiting confirmation") || !strings.Contains(out, "https://app.example.com/review") {
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

func TestCmdApplyJSONReturnsInitialChangeRequest(t *testing.T) {
	pkg := testApplyPackage(t)
	var mutations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/capabilities":
			_, _ = w.Write([]byte(`{"deployment_change_requests":true}`))
		case "/api/packages/telos/demo/versions/1.2.3":
			_ = json.NewEncoder(w).Encode(map[string]string{"scope": "telos", "name": "demo", "version": "1.2.3", "ref": "@telos/demo:1.2.3", "digest": pkg.Digest})
		case "/api/packages/telos/demo/versions/1.2.3/bundle":
			_, _ = w.Write(pkg.Bytes)
		case "/api/deployments":
			mutations++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["require_confirmation"] != true || r.Header.Get("X-Telos-Change-Requests") != "1" {
				t.Errorf("initial confirmation missing: body=%#v headers=%v", body, r.Header)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"outcome":"requested","deployment":{"id":"sess_new","name":"demo","state":"pending","status":"working","current_revision_id":""},"change_request":{"id":"req_initial","deployment_id":"sess_new","action":"create","status":"queued","require_confirmation":true,"review_url":"https://custom.example.com/review"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)
	out := captureStdout(t, func() {
		cmdApply([]string{"@telos/demo:1.2.3", "--require-confirmation", "--json"})
	})
	var receipt struct {
		Operation     string                    `json:"operation"`
		Session       cloud.SessionRecord       `json:"session"`
		ChangeRequest cloud.ChangeRequestRecord `json:"change_request"`
		ReviewURL     string                    `json:"review_url"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Operation != "requested" || receipt.ChangeRequest.ID != "req_initial" || !receipt.ChangeRequest.RequireConfirmation || receipt.ReviewURL != "https://custom.example.com/review" || receipt.Session.CurrentRevisionID != "" || mutations != 1 {
		t.Fatalf("initial request receipt=%s mutations=%d", out, mutations)
	}
}

func TestApplyDoesNotOverrideExistingConfirmationPolicy(t *testing.T) {
	requireConfirmation := false
	_, _, err := applyCloudSessionPackage(cloud.NewClient("https://invalid.example", "token"), "demo", "@telos/demo:1.0.0", "sess_123", sessionRuntimeConfig{}, false, &requireConfirmation)
	if err == nil || !strings.Contains(err.Error(), "only configure a new Cloud deployment") {
		t.Fatalf("existing policy override accepted: %v", err)
	}
}

func TestInitialRequestDoesNotAppearAsADeployedRevision(t *testing.T) {
	session := cloud.SessionRecord{ID: "sess_new", State: "pending", Status: "working", StatusReason: "Awaiting initial confirmation", PackageDigest: "sha256:proposal"}
	var out bytes.Buffer
	printCloudSessionDescription(&out, session)
	if !strings.Contains(out.String(), "Revision  not deployed yet") || !strings.Contains(out.String(), "Awaiting initial confirmation") || strings.Contains(out.String(), "sha256:proposal") {
		t.Fatalf("pending creation presented as deployed: %s", out.String())
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/deployments/sess_new" {
			t.Errorf("pending creation attempted a deployed bundle read: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(session)
	}))
	defer srv.Close()
	_, err := packageForSession(cloud.NewClient(srv.URL, "token"), "sess_new")
	if err == nil || !strings.Contains(err.Error(), "no deployed revision yet") {
		t.Fatalf("pending creation accepted as current revision: %v", err)
	}
}
