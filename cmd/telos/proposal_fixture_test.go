package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/spec"
)

const (
	testBaseSpec = "---\nname: reading-list\nversion: 0.1.0\ninterval: 6h\n---\n# Goal\nKeep a reading list.\n" +
		"# Acceptance\n- Books remain available when the application restarts.\n"
	// testProposedSpec rewrites line 7 and adds line 10.
	testProposedSpec = "---\nname: reading-list\nversion: 0.1.0\ninterval: 6h\n---\n# Goal\nKeep a shared reading list.\n" +
		"# Acceptance\n- Books remain available when the application restarts.\n" +
		"- Every Friday, email each reader a digest of new books.\n"
)

// testProposal is the proposal record Cloud returns for p_3f9a2b7c.
func testProposal(status string) map[string]any {
	return map[string]any{
		"id":            "p_3f9a2b7c",
		"goal":          "goal_c7d2f0a4e8",
		"goal_name":     "reading-list",
		"status":        status,
		"title":         "Add a weekly digest",
		"update_number": 2,
		"updated_at":    time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339),
		"url":           "https://usetelos.ai/p",
		"preview": map[string]any{
			"base_spec":     testBaseSpec,
			"proposed_spec": testProposedSpec,
			"files":         []any{},
		},
	}
}

// respondJSON is a fake Cloud route that answers with value.
func respondJSON(value any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(value)
	}
}

// respondError is a fake Cloud route that refuses with a stable error code.
func respondError(status int, code, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
	}
}

// decodeBody reads a fake Cloud request's JSON body.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode %s %s: %v", r.Method, r.URL.Path, err)
	}
	return body
}

// proposalCLITest points the CLI at a fake Cloud for one test.
func proposalCLITest(t *testing.T, routes map[string]http.HandlerFunc) {
	t.Helper()
	server := inferenceTestServer(t, routes)
	t.Cleanup(server.Close)
	configureCloudTest(t, server.URL)
	for _, name := range []string{"TELOS_CONTEXT", "TELOS_SESSION_ID", "TELOS_RUNTIME"} {
		t.Setenv(name, "")
	}
}

func TestProposalCommandsRequireLogin(t *testing.T) {
	configureLocalOnlyTest(t)
	for _, args := range [][]string{
		{"propose", "--onto", "p_3f9a2b7c", "-t", "Title"},
		{"review", "p_3f9a2b7c", "--approved"},
		{"comment", "p_3f9a2b7c", "-m", "Hi"},
		{"apply", "p_3f9a2b7c"},
		{"delete", "p_3f9a2b7c"},
		{"describe", "p_3f9a2b7c"},
		{"pull", "p_3f9a2b7c"},
		{"list", "proposals"},
	} {
		_, stderr, err := inferenceCLIProcess(t, args...)
		if want := "error: this command uses Telos Cloud; run `telos login` first\n"; err == nil || stderr != want {
			t.Fatalf("%v: err=%v stderr=%q, want %q", args, err, stderr, want)
		}
	}
}

// testSkillSpec compiles a spec named demo that loads the local skill notes,
// whose SKILL.md says "Keep notes short." on line 5.
func testSkillSpec(t *testing.T) *spec.CompiledEnvironment {
	t.Helper()
	root := t.TempDir()
	for path, text := range map[string]string{
		"SPEC.md":               "---\nname: demo\nversion: 1.2.3\nskills:\n  - ./skills/notes\n---\n# Goal\nServe a demo.\n",
		"skills/notes/SKILL.md": "---\nname: notes\ndescription: Notes\n---\nKeep notes short.\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	compiled, err := spec.CompileEnvironment(filepath.Join(root, "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
