package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestCloudSkillReviewSurvivesDecodingAndOutput(t *testing.T) {
	// Current Cloud preview fields, including a skill-only change and an
	// incomplete comparison. None of these details may disappear before approval.
	const body = `{"id":"cr_review","kind":"change_request","deployment_id":"sess_123","status":"awaiting_confirmation","mode":"saved","update_number":1,"prepared_plan_id":"cp_review","preview":{
		"base_spec":"unchanged","proposed_spec":"unchanged","base_update_number":1,
		"base_skills":[],"proposed_skills":[{"name":"review","ref":"@personal/draft:1.0.0","digest":"sha256:new","starred":true,"draft":true,"published_ref":"@personal/review:1.0.1","paths":["SKILL.md"]}],
		"files":[{"path":"skills/review/SKILL.md","status":"modified","base_text":"Old instructions.\n","proposed_text":"Review every change.\n","base_mode":"0644","proposed_mode":"0755"},{"path":"skills/review/icon.png","status":"added","omitted_reason":"binary"}],
		"skill_warnings":[{"name":"large-skill","reason":"limit"}]},"creation":{"name":"demo","secret_ids":[],"network_rules":[{"host":"example.com","action":"allow"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer server.Close()
	client := cloud.NewClient(server.URL, "token")
	request, err := client.GetChangeRequest("sess_123", "cr_review")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	printDeploymentPlan(&output, client, request)
	for _, want := range []string{"No spec changes", "-Old instructions.", "+Review every change.", "0644 -> 0755", "Content omitted: binary", `skill "large-skill" preview is incomplete (limit)`, "example.com"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("terminal review lost %q: %s", want, output.String())
		}
	}
	encoded := captureStdout(t, func() { printDeploymentPlanJSON(client, request, "") })
	var receipt struct {
		Request cloud.ChangeRequestRecord `json:"change_request"`
	}
	if err := json.Unmarshal([]byte(encoded), &receipt); err != nil {
		t.Fatal(err)
	}
	preview := receipt.Request.Preview
	if preview == nil || len(preview.Files) != 2 || preview.Files[0].ProposedText == nil || *preview.Files[0].ProposedText != "Review every change.\n" || len(preview.SkillWarnings) != 1 || preview.SkillWarnings[0].Reason != "limit" || preview.BaseUpdateNumber == nil || *preview.BaseUpdateNumber != 1 {
		t.Fatalf("JSON lost review details: %s", encoded)
	}
	skill := preview.ProposedSkills[0]
	if !skill.Draft || skill.PublishedRef == nil || *skill.PublishedRef != "@personal/review:1.0.1" || len(skill.Paths) != 1 || skill.Paths[0] != "SKILL.md" || len(receipt.Request.Creation.NetworkRules) != 1 {
		t.Fatalf("JSON lost proposed artifact or network settings: %s", encoded)
	}
}

func TestCreatePlanRejectsAnotherGoal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/deployments/sess_123":
			_, _ = io.WriteString(w, `{"id":"sess_123","current_revision_id":"rev_7"}`)
		case "/api/deployment-plans":
			request := testDeploymentPlan("saved", "awaiting_confirmation")
			request.DeploymentID = "sess_other"
			_ = json.NewEncoder(w).Encode(request)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(path, []byte(testPlanSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := createCloudPlan(cloud.NewClient(server.URL, "token"), cloudPlanInput{specArg: path, sessionID: "sess_123", mode: "saved", revisionMessage: "Update"})
	if err == nil || !strings.Contains(err.Error(), "different Goal") {
		t.Fatalf("err=%v", err)
	}
}

func TestPlanReviewEscapesTerminalControlsAndPreservesJSON(t *testing.T) {
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	content := "dangerous command\r\x1b[2J\x1b[Hlooks harmless\b\a\u202e\n"
	request.Message = "Review\x1b[2J"
	request.Preview.ProposedSpec += content
	request.Preview.Files = []cloud.DeploymentPlanFile{{
		Path: "skills/\x1b[2J/\nSKILL.md", Status: "added", ProposedText: &content,
	}}
	client := cloud.NewClient("https://api.example.test", "token")
	var output bytes.Buffer
	printDeploymentPlan(&output, client, &request)
	if strings.ContainsAny(output.String(), "\r\x1b\b\a\u202e") {
		t.Fatalf("review contains terminal controls: %q", output.String())
	}
	for _, want := range []string{`Review\x1b[2J`, `+dangerous command\r\x1b[2J\x1b[Hlooks harmless\b\a\u202e`, `proposed/skills/\x1b[2J/\nSKILL.md`} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("review did not visibly preserve %q: %s", want, output.String())
		}
	}
	encoded := captureStdout(t, func() { printDeploymentPlanJSON(client, &request, "") })
	var receipt struct {
		Request cloud.ChangeRequestRecord `json:"change_request"`
	}
	if err := json.Unmarshal([]byte(encoded), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Request.Preview == nil || len(receipt.Request.Preview.Files) != 1 || *receipt.Request.Preview.Files[0].ProposedText != content || receipt.Request.Message != request.Message {
		t.Fatalf("JSON changed original review content: %s", encoded)
	}
}
