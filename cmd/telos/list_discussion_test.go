package main

import (
	"net/http"
	"testing"

	"github.com/telos-org/telos/internal/spec"
)

func TestListProposalsPassesFiltersToCloudAndReadsEveryPage(t *testing.T) {
	older := testProposal("pending")
	older["id"], older["title"] = "p_1a2b", "Add tags"
	var limits []string
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals": func(w http.ResponseWriter, r *http.Request) {
			query := r.URL.Query()
			if query.Get("goal") != "goal_c7d2f0a4e8" || query.Get("status") != "pending,stale" {
				t.Errorf("proposals query = %s", r.URL.RawQuery)
			}
			limits = append(limits, query.Get("limit"))
			if query.Get("cursor") == "" {
				respondJSON(map[string]any{"proposals": []any{testProposal("pending")}, "next_cursor": 7})(w, r)
				return
			}
			respondJSON(map[string]any{"proposals": []any{older}, "next_cursor": nil})(w, r)
		},
	})
	// A repeated status matches any of them.
	filter := []string{"proposals", "--filter", "goal=goal_c7d2f0a4e8,status=Pending,status=stale"}
	out := captureStdout(t, func() { cmdList(filter) })
	want := "TITLE                STATUS   UPDATED  ID\n" +
		"Add a weekly digest  Pending  2m ago   p_3f9a2b7c\n" +
		"Add tags             Pending  2m ago   p_1a2b\n"
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}

	limits = nil
	out = captureStdout(t, func() { cmdList(append(filter, "--limit", "1")) })
	if want := "TITLE                STATUS   UPDATED  ID\nAdd a weekly digest  Pending  2m ago   p_3f9a2b7c\n"; out != want || len(limits) != 1 || limits[0] != "1" {
		t.Fatalf("--limit 1: requests with limit %q, output:\n%s", limits, out)
	}
}

func TestListCommentsNamesTheLineEachIsOn(t *testing.T) {
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
		"GET /api/comments": func(w http.ResponseWriter, r *http.Request) {
			if query := r.URL.Query(); query.Get("proposal") != "p_3f9a2b7c" || query.Get("status") != "open" {
				t.Errorf("comments query = %s", r.URL.RawQuery)
			}
			respondJSON(map[string]any{"comments": []any{
				map[string]any{
					"id":      "c_4d7a1f0e",
					"author":  map[string]string{"email": "admin@example.com"},
					"anchor":  map[string]any{"path": "SPEC.md", "quote": "- Every Friday, email each reader a digest of new books.", "side": "new"},
					"replies": []any{map[string]any{"id": "c_5e8b"}},
				},
				map[string]any{"id": "c_6f9c", "author": map[string]string{"email": "owner@example.com"}},
			}})(w, r)
		},
	})
	out := captureStdout(t, func() { cmdList([]string{"comments", "--filter", "proposal=p_3f9a2b7c,status=open"}) })
	want := "ID          AUTHOR             ON               REPLIES\n" +
		"c_4d7a1f0e  admin@example.com  SPEC.md line 10  1\n" +
		"c_6f9c      owner@example.com  -                0\n"
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}

func TestListCommentsNumbersLinesInFilesTheProposalLeavesUnchanged(t *testing.T) {
	pkg, err := spec.BuildApplyPackage(testSkillSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
		"GET /api/proposals/p_3f9a2b7c/bundle": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Telos-Package-Digest", pkg.Digest)
			_, _ = w.Write(pkg.Bytes)
		},
		"GET /api/comments": respondJSON(map[string]any{"comments": []any{map[string]any{
			"id":     "c_4d7a1f0e",
			"author": map[string]string{"email": "admin@example.com"},
			"anchor": map[string]any{"path": "skills/notes/SKILL.md", "quote": "Keep notes short.", "side": "new"},
		}}}),
	})
	out := captureStdout(t, func() { cmdList([]string{"comments", "--filter", "proposal=p_3f9a2b7c"}) })
	if want := "ID          AUTHOR             ON                            REPLIES\nc_4d7a1f0e  admin@example.com  skills/notes/SKILL.md line 5  0\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestListRefusesFiltersItDoesNotTake(t *testing.T) {
	proposalCLITest(t, nil)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"comments", "--filter", "on=p_3f9a2b7c"}, "error: comments can't be filtered by \"on\"; use goal, proposal, status, author\n"},
		{[]string{"goals", "--filter", "author=me"}, "error: goals can't be filtered by \"author\"; use status\n"},
		{[]string{"comments", "--filter", "status=open"}, "error: telos list comments requires --filter goal=GOAL or --filter proposal=PROPOSAL\n"},
		{[]string{"proposals", "--wide"}, "error: --wide, --local, and --cloud apply only to Goals\n"},
		{[]string{"changes"}, "error: unknown listing \"changes\"; use goals, proposals, or comments\n"},
		{[]string{"proposals", "--filter", "goal=g_a,goal=g_b"}, "error: --filter takes goal once\n"},
	} {
		_, stderr, err := inferenceCLIProcess(t, append([]string{"list"}, tt.args...)...)
		if err == nil || stderr != tt.want {
			t.Fatalf("%v: err=%v stderr=%q, want %q", tt.args, err, stderr, tt.want)
		}
	}
}

func TestListGoalCommentsOnFieldsDownloadsNoFiles(t *testing.T) {
	// The Goal's package routes are left out: any download fails the test.
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/comments": respondJSON(map[string]any{"comments": []any{map[string]any{
			"id":     "c_4d7a1f0e",
			"author": map[string]string{"email": "admin@example.com"},
			"anchor": map[string]any{"path": "SPEC.md", "field": "interval", "quote": "6h"},
		}}}),
	})
	out := captureStdout(t, func() { cmdList([]string{"comments", "--filter", "goal=goal_c7d2f0a4e8"}) })
	if want := "ID          AUTHOR             ON                REPLIES\nc_4d7a1f0e  admin@example.com  SPEC.md interval  0\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestListGoalsFiltersByStatus(t *testing.T) {
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/deployments": respondJSON(map[string]any{"deployments": []any{
			map[string]any{"id": "goal_a", "name": "reading-list", "status": "working"},
			map[string]any{"id": "goal_b", "name": "inbox", "status": "blocked"},
		}}),
	})
	out := captureStdout(t, func() { cmdList([]string{"goals", "--filter", "status=blocked"}) })
	if want := "NAME   STATUS   ID\ninbox  blocked  goal_b\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}
