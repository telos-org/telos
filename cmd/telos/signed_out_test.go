package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestSignedOutCloudCommandsRequireLogin(t *testing.T) {
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_SESSION_ID", "")
	t.Setenv("TELOS_CONTEXT", "")
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nname: example\nversion: 1.0.0\n---\n# Goal\nTest.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"describe", "goal_example"},
		{"describe", "goal_example", "--json"},
		{"describe", "goal_example", "--context", "personal"},
		{"describe", "opaque-cloud-id"},
		{"logs", "goal_example"},
		{"logs", "goal_example", "--json"},
		{"logs", "goal_example", "--raw"},
		{"logs", "goal_example", "--follow"},
		{"logs", "goal_example", "--context", "personal"},
		{"logs", "opaque-cloud-id"},
		{"get", "goal_example"},
		{"delete", "goal_example"},
		{"list", "--cloud"},
		{"list", "--context", "@team"},
		{"config", "--context", "@team"},
		{"push", specPath},
		{"plan", specPath, "--goal", "goal_example"},
		{"apply", "--goal", "goal_example", "--thinking", "high"},
		{"apply", specPath},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, stderr, err := inferenceCLIProcess(t, args...)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("exit = %v, stdout = %q, stderr = %q", err, out, stderr)
			}
			want := "error: not logged in; run `telos login` first\n"
			if args[0] == "apply" && args[1] == specPath {
				want = "error: telos apply deploys to Telos Cloud; run `telos login` first\n"
			}
			if out != "" || stderr != want {
				t.Fatalf("stdout = %q, stderr = %q", out, stderr)
			}
		})
	}
}

func TestSignedOutGoalReadsValidateArgumentsBeforeLogin(t *testing.T) {
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_ID", "")
	t.Setenv("TELOS_CONTEXT", "")
	for _, args := range [][]string{
		{"describe"},
		{"logs"},
		{"logs", "goal_example", "--raw", "--json"},
		{"logs", "goal_example", "--tail", "0"},
		{"describe", "local_example", "--context", "personal"},
		{"logs", "local_example", "--context", "personal"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, stderr, err := inferenceCLIProcess(t, args...)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 || out != "" || strings.Contains(stderr, "telos login") {
				t.Fatalf("exit = %v, stdout = %q, stderr = %q", err, out, stderr)
			}
		})
	}
}

func TestRuntimeGoalReadsDoNotRequireCloudLogin(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer scoped-token" {
					t.Error("runtime lookup lost its scoped token")
				}
				switch r.URL.Path {
				case "/api/sessions/sess_child":
					if status != http.StatusOK {
						http.Error(w, "runtime lookup failed", status)
						return
					}
					_ = json.NewEncoder(w).Encode(sessionapi.Session{
						SessionID: "sess_child", Runtime: sessionapi.RuntimeCloud, Status: sessionapi.StatusCompleted,
					})
				case "/api/sessions/sess_child/transcript":
					_, _ = w.Write([]byte("Runtime progress.\n"))
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			configureLocalOnlyTest(t)
			t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
			t.Setenv("TELOS_SESSION_ID", "sess_parent")
			t.Setenv("TELOS_API_TOKEN", "scoped-token")
			t.Setenv("TELOS_API_ENDPOINT", server.URL)
			t.Setenv("TELOS_RUNTIME", "cloud")
			t.Setenv("TELOS_CONTEXT", "")
			for _, args := range [][]string{{"describe", "sess_child"}, {"logs", "sess_child", "--raw"}} {
				out, stderr, err := inferenceCLIProcess(t, args...)
				if status == http.StatusOK {
					if err != nil || out == "" || stderr != "" {
						t.Fatalf("%v: exit = %v, stdout = %q, stderr = %q", args, err, out, stderr)
					}
					continue
				}
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || out != "" ||
					!strings.Contains(stderr, "root session lookup failed") || strings.Contains(stderr, "telos login") {
					t.Fatalf("%v: exit = %v, stdout = %q, stderr = %q", args, err, out, stderr)
				}
			}
		})
	}
}

func TestSignedOutLocalGoalReadsDoNotRequireLogin(t *testing.T) {
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_SESSION_ID", "")
	t.Setenv("TELOS_CONTEXT", "")

	markdown := "---\nname: example\nversion: 1.0.0\n---\n# Goal\nTest.\n"
	session, err := store().Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatal(err)
	}
	transcript := "# Transcript\n<progress_update>Local progress.</progress_update>\n"
	if err := os.WriteFile(*session.Specs[0].TranscriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"describe", "logs"} {
		t.Run(command, func(t *testing.T) {
			out, stderr, err := inferenceCLIProcess(t, command, session.SessionID)
			want := session.SessionID
			if command == "logs" {
				want = "Local progress."
			}
			if err != nil || stderr != "" || !strings.Contains(out, want) {
				t.Fatalf("existing local Goal: exit = %v, stdout = %q, stderr = %q", err, out, stderr)
			}

			out, stderr, err = inferenceCLIProcess(t, command, "local_missing")
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || out != "" ||
				!strings.Contains(stderr, "Local Goals are workspace-scoped") || strings.Contains(stderr, "telos login") {
				t.Fatalf("missing local Goal: exit = %v, stdout = %q, stderr = %q", err, out, stderr)
			}
		})
	}
}
