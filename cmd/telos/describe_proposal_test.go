package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestDescribeProposalShowsReviewsAndNumberedChanges(t *testing.T) {
	admin := map[string]string{"subject": "u_admin", "email": "admin@example.com"}
	owner := map[string]string{"subject": "u_owner", "email": "owner@example.com"}
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
		"GET /api/proposals/p_3f9a2b7c/history": respondJSON(map[string]any{"reviews": []any{
			map[string]any{"reviewer": admin, "update_number": 1, "verdict": "approved"},
			map[string]any{"reviewer": owner, "update_number": 2, "verdict": "approved"},
			map[string]any{"reviewer": admin, "update_number": 2, "verdict": "approved"},
			map[string]any{"reviewer": admin, "update_number": 2, "verdict": "blocked", "previous": "approved"},
			map[string]any{"reviewer": owner, "update_number": 2, "verdict": nil, "previous": "approved"},
		}}),
	})

	out := captureStdout(t, func() { cmdDescribe([]string{"p_3f9a2b7c"}) })
	want := `Title     Add a weekly digest
Status    Pending
Goal      reading-list (goal_c7d2f0a4e8)
Updated   2m ago

Reviews
  admin@example.com  blocked

SPEC.md
   4   4    interval: 6h
   5   5    ---
   6   6    # Goal
   7      - Keep a reading list.
       7  + Keep a shared reading list.
   8   8    # Acceptance
   9   9    - Books remain available when the application restarts.
      10  + - Every Friday, email each reader a digest of new books.
`
	if out != want {
		t.Fatalf("describe output:\n%s\nwant:\n%s", out, want)
	}
}

func TestDescribeProposalNamesFilesAndSkillsItCannotShow(t *testing.T) {
	proposal := testProposal("pending")
	proposal["preview"] = map[string]any{
		"base_spec":      testBaseSpec,
		"proposed_spec":  testBaseSpec,
		"files":          []any{map[string]any{"path": "skills/logo/logo.png", "omitted_reason": "binary"}},
		"skill_warnings": []any{map[string]any{"name": "notes", "reason": "too_large"}},
	}
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals/p_3f9a2b7c":         respondJSON(proposal),
		"GET /api/proposals/p_3f9a2b7c/history": respondJSON(map[string]any{"reviews": []any{}}),
	})
	out := captureStdout(t, func() { cmdDescribe([]string{"p_3f9a2b7c"}) })
	if want := "\nskills/logo/logo.png\n  Not shown: binary\n\nSkills not compared\n  notes  too large\n"; !strings.HasSuffix(out, want) {
		t.Fatalf("describe output:\n%s\nwant it to end with:\n%s", out, want)
	}
}

func TestDescribeRestoreProposalNamesTheRevisionAndShowsItsChanges(t *testing.T) {
	restore := testProposal("pending")
	restore["action"], restore["source_revision_id"] = "restore", "rev_8f21c47a91ee"
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals/p_3f9a2b7c":         respondJSON(restore),
		"GET /api/proposals/p_3f9a2b7c/history": respondJSON(map[string]any{"reviews": []any{}}),
	})
	out := captureStdout(t, func() { cmdDescribe([]string{"p_3f9a2b7c"}) })
	for _, want := range []string{
		"Goal      reading-list (goal_c7d2f0a4e8)\nRestores  rev_8f21c47a91ee\nUpdated   2m ago\n",
		"      10  + - Every Friday, email each reader a digest of new books.\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe output missing %q:\n%s", want, out)
		}
	}
}
