package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// proposeTestRoutes is a fake Cloud for telos propose. It stages packages and
// skills at refs derived from their content, as Cloud does, and has no
// Registry publish routes: proposing must never publish a version.
func proposeTestRoutes(t *testing.T, extra map[string]http.HandlerFunc) map[string]http.HandlerFunc {
	stage := func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		sum := sha256.Sum256(fmt.Append(nil, body["data_base64"], body["files"]))
		respondJSON(map[string]any{"ref": fmt.Sprintf("@org-staging/%s:0.0.0-plan.sha256-%x", body["name"], sum[:6])})(w, r)
	}
	routes := map[string]http.HandlerFunc{
		"GET /api/deployments": respondJSON(map[string]any{"deployments": []any{
			map[string]any{"id": "goal_c7d2f0a4e8", "name": "example", "state": "running"},
		}}),
		"POST /api/staging/packages":    stage,
		"POST /api/staging/skills":      stage,
		"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
	}
	for route, handler := range extra {
		routes[route] = handler
	}
	return routes
}

func TestProposeOpensAProposalOnTheGoalTheSpecNames(t *testing.T) {
	proposalCLITest(t, proposeTestRoutes(t, map[string]http.HandlerFunc{
		"POST /api/proposals": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			if body["goal"] != "goal_c7d2f0a4e8" || body["title"] != "Add a weekly digest" || !strings.HasPrefix(fmt.Sprint(body["package_ref"]), "@org-staging/example:") {
				t.Errorf("open body = %#v", body)
			}
			respondJSON(testProposal("pending"))(w, r)
		},
	}))
	out, stderr, err := applyTestProcess(t, applyTestSpec(t), "propose", "SPEC.md", "-t", "Add a weekly digest")
	want := "opened proposal p_3f9a2b7c for reading-list\n" +
		"View: https://usetelos.ai/p\n" +
		"To update it: telos propose SPEC.md --onto p_3f9a2b7c -m \"MSG\"\n"
	if err != nil || out != want {
		t.Fatalf("err=%v stderr=%q stdout=%q, want %q", err, stderr, out, want)
	}
}

func TestProposeOntoAddsAnUpdateAndRenamesInOneRequest(t *testing.T) {
	proposalCLITest(t, proposeTestRoutes(t, map[string]http.HandlerFunc{
		"POST /api/proposals/p_3f9a2b7c/updates": func(w http.ResponseWriter, r *http.Request) {
			body := decodeBody(t, r)
			if body["expected_update_number"] != 2.0 || body["message"] != "Send on Mondays" ||
				!strings.HasPrefix(fmt.Sprint(body["package_ref"]), "@org-staging/example:") || body["title"] != "Add a Monday digest" {
				t.Errorf("update body = %#v", body)
			}
			renamed := testProposal("pending")
			renamed["title"] = "Add a Monday digest"
			respondJSON(renamed)(w, r)
		},
	}))
	out, stderr, err := applyTestProcess(t, applyTestSpec(t), "propose", "SPEC.md", "--onto", "p_3f9a2b7c", "-m", "Send on Mondays", "-t", "Add a Monday digest")
	want := "updated p_3f9a2b7c\nrenamed p_3f9a2b7c to \"Add a Monday digest\"\n"
	if err != nil || out != want {
		t.Fatalf("err=%v stderr=%q stdout=%q, want %q", err, stderr, out, want)
	}
}

func TestProposeOntoAStaleProposalSaysHowToStartOver(t *testing.T) {
	want := "error: p_3f9a2b7c is stale: reading-list changed since it was opened\n" +
		"Pull the latest version, redo your change, and propose again:\n" +
		"  telos pull goal_c7d2f0a4e8\n" +
		"  telos propose reading-list/SPEC.md -t \"Add a weekly digest\"\n"
	proposalCLITest(t, proposeTestRoutes(t, map[string]http.HandlerFunc{
		"PATCH /api/proposals/p_3f9a2b7c": respondError(http.StatusConflict, "proposal_stale", "\"Add a weekly digest\" is stale because reading-list changed since it was opened"),
	}))
	_, stderr, err := inferenceCLIProcess(t, "propose", "--onto", "p_3f9a2b7c", "-t", "Add a Monday digest")
	if err == nil || stderr != want {
		t.Fatalf("err=%v stderr=%q, want %q", err, stderr, want)
	}
}

func TestProposeStagesEachUpdatePrivately(t *testing.T) {
	var refs []string
	var stagedSkills []any
	proposalCLITest(t, proposeTestRoutes(t, map[string]http.HandlerFunc{
		"POST /api/staging/skills": func(w http.ResponseWriter, r *http.Request) {
			stagedSkills = append(stagedSkills, decodeBody(t, r)["name"])
			respondJSON(map[string]any{"ref": "@org-staging/notes:0.0.0-plan.sha256-1a2b"})(w, r)
		},
		"POST /api/proposals/p_3f9a2b7c/updates": func(w http.ResponseWriter, r *http.Request) {
			refs = append(refs, fmt.Sprint(decodeBody(t, r)["package_ref"]))
			respondJSON(testProposal("pending"))(w, r)
		},
	}))
	dir := applyTestSpec(t)
	skill := filepath.Join(dir, "skills", "notes", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: notes\ndescription: Notes\n---\nKeep notes short.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two updates at the same spec version, each with different content.
	for _, goal := range []string{"Send on Mondays.", "Send on Fridays."} {
		spec := "---\nname: example\nversion: 1.0.0\nskills:\n  - ./skills/notes\n---\n# Goal\n" + goal + "\n# Acceptance\n- Test passes.\n"
		if err := os.WriteFile(filepath.Join(dir, "SPEC.md"), []byte(spec), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, stderr, err := applyTestProcess(t, dir, "propose", "SPEC.md", "--onto", "p_3f9a2b7c", "-m", goal); err != nil {
			t.Fatalf("propose %q: %v %s", goal, err, stderr)
		}
	}
	if len(refs) != 2 || refs[0] == refs[1] || len(stagedSkills) != 2 || stagedSkills[0] != "notes" {
		t.Fatalf("staged package refs %q, staged skills %v", refs, stagedSkills)
	}
}
