package cloud

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeploymentPlansUseCloudContractAndPrivateArtifactRoutes(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("X-Telos-Org-Id") != "org_acme" {
			t.Errorf("missing scoped authorization: %v", r.Header)
		}
		switch r.URL.Path {
		case "/api/deployment-plans/access":
			_, _ = w.Write([]byte(`{"can_plan":true,"can_apply":false}`))
		case "/api/deployment-plans":
			var options DeploymentPlanOptions
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
			}
			if options.Mode != "saved" || options.Create != nil || options.Update.ExpectedCurrentRevisionID != "rev_7" {
				t.Errorf("unexpected options: %+v", options)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"cr_1","deployment_id":"sess_1","mode":"saved","status":"awaiting_confirmation","base_revision_id":"rev_7","preview":{"base_spec":"old","proposed_spec":"new","base_skills":[],"proposed_skills":[{"name":"coding","ref":"@acme/coding:1.0.0","digest":"sha256:123","starred":true}]}}`))
		case "/api/deployments/sess_1/change-requests/cr_1/confirm":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["expected_current_revision_id"] != "rev_7" {
				t.Errorf("lost reviewed baseline: %v", body)
			}
			_, _ = w.Write([]byte(`{"id":"cr_1","deployment_id":"sess_1","mode":"saved","status":"applied"}`))
		case "/api/deployment-plans/packages", "/api/packages":
			_, _ = w.Write([]byte(`{"ref":"@acme/private-plan:1.0.0","digest":"sha256:123"}`))
		case "/api/deployment-plans/skills", "/api/skills":
			_, _ = w.Write([]byte(`{"ref":"@acme/private-skill:1.0.0","digest":"sha256:123"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "token")
	client.OrgID = "org_acme"
	access, err := client.DeploymentPlanAccess("sess_1")
	if err != nil || !access.CanPlan || access.CanApply {
		t.Fatalf("access=%+v err=%v", access, err)
	}
	request, err := client.CreateDeploymentPlan(DeploymentPlanOptions{Mode: "saved", DeploymentID: "sess_1", Update: &SessionUpdateOptions{PackageRef: "@acme/app:1.0.0", ExpectedCurrentRevisionID: "rev_7"}})
	if err != nil || request.Preview == nil || len(request.Preview.ProposedSkills) != 1 || !request.Preview.ProposedSkills[0].Starred {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	if _, err := client.ConfirmChangeRequest(*request); err != nil {
		t.Fatal(err)
	}
	staging := client.PlanArtifactClient()
	for _, c := range []*Client{staging, client} {
		if _, err := c.PublishPackage("", "app", "1.0.0", []byte("package")); err != nil {
			t.Fatal(err)
		}
		if _, err := c.PublishSkillVersion("", "coding", "1.0.0", map[string]SkillFile{}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"GET /api/deployment-plans/access?deployment_id=sess_1", "POST /api/deployment-plans", "POST /api/deployments/sess_1/change-requests/cr_1/confirm", "POST /api/deployment-plans/packages", "POST /api/deployment-plans/skills", "POST /api/packages", "POST /api/skills"}
	if len(calls) != len(want) {
		t.Fatalf("calls=%v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d=%q want %q", i, calls[i], want[i])
		}
	}
}

func TestConfirmCreateSendsExplicitNullBaseline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if string(body["expected_current_revision_id"]) != "null" {
			t.Errorf("create baseline must be explicit null: %v", body)
		}
		_, _ = w.Write([]byte(`{"id":"cr_1","deployment_id":"sess_1","mode":"saved","status":"applying"}`))
	}))
	defer server.Close()
	_, err := NewClient(server.URL, "token").ConfirmChangeRequest(ChangeRequestRecord{ID: "cr_1", DeploymentID: "sess_1"})
	if err != nil {
		t.Fatal(err)
	}
}
