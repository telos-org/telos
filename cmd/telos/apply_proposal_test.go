package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestApplyProposalDeploysItAndShowsTheGoal(t *testing.T) {
	proposalCLITest(t, map[string]http.HandlerFunc{
		"POST /api/proposals/p_3f9a2b7c/apply": func(w http.ResponseWriter, r *http.Request) {
			if body := decodeBody(t, r); body["force"] != false {
				t.Errorf("apply body = %#v", body)
			}
			respondJSON(testProposal("applied"))(w, r)
		},
		"GET /api/deployments/goal_c7d2f0a4e8": respondJSON(map[string]any{
			"id": "goal_c7d2f0a4e8", "name": "reading-list", "state": "running", "status": "working", "package_digest": "sha256:3211e85fe81b0000",
		}),
	})
	out := captureStdout(t, func() { cmdApply([]string{"p_3f9a2b7c"}) })
	for _, want := range []string{"accepted \"Add a weekly digest\" for reading-list\n\n", "Status    working\n", "Goal      goal_c7d2f0a4e8\n", "Logs      telos logs goal_c7d2f0a4e8\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("apply output missing %q:\n%s", want, out)
		}
	}
}

func TestApplyProposalWaitingForASnapshotSuggestsForce(t *testing.T) {
	waiting := testProposal("pending")
	waiting["error_code"] = "snapshot_pending"
	proposalCLITest(t, map[string]http.HandlerFunc{
		"POST /api/proposals/p_3f9a2b7c/apply": respondJSON(waiting),
	})
	_, stderr, err := inferenceCLIProcess(t, "apply", "p_3f9a2b7c")
	if err == nil || !strings.HasSuffix(stderr, "To deploy anyway, retry the same command with --force.\n") {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
}

func TestApplyRestoreProposalReportsItInProgress(t *testing.T) {
	restore := testProposal("applying")
	restore["action"] = "restore"
	proposalCLITest(t, map[string]http.HandlerFunc{
		"POST /api/proposals/p_3f9a2b7c/apply": respondJSON(restore),
		"GET /api/deployments/goal_c7d2f0a4e8": respondJSON(map[string]any{
			"id": "goal_c7d2f0a4e8", "name": "reading-list", "state": "running", "status": "working",
		}),
	})
	out, stderr, err := inferenceCLIProcess(t, "apply", "p_3f9a2b7c")
	if err != nil || !strings.HasPrefix(out, "applying \"Add a weekly digest\" to reading-list\n\nStatus    working\n") ||
		!strings.Contains(out, "Logs      telos logs goal_c7d2f0a4e8\n") {
		t.Fatalf("err=%v stderr=%q stdout:\n%s", err, stderr, out)
	}
}

func TestApplyProposalReportsAnApplyThatDidNotFinish(t *testing.T) {
	failed := testProposal("failed")
	failed["error"], failed["error_code"] = "The Goal's runtime rejected the update.", "runtime_update_rejected"
	for _, tt := range []struct {
		name   string
		record map[string]any
		want   string
	}{
		{"failed", failed, "error: The Goal's runtime rejected the update.\n"},
		{"stale", testProposal("stale"), "error: \"Add a weekly digest\" is stale because reading-list changed since it was opened\n" +
			"To start over: telos pull goal_c7d2f0a4e8, then telos propose SPEC.md -t \"TITLE\"\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			proposalCLITest(t, map[string]http.HandlerFunc{
				"POST /api/proposals/p_3f9a2b7c/apply": respondJSON(tt.record),
			})
			out, stderr, err := inferenceCLIProcess(t, "apply", "p_3f9a2b7c")
			if err == nil || out != "" || stderr != tt.want {
				t.Fatalf("err=%v stdout=%q stderr=%q, want %q", err, out, stderr, tt.want)
			}
		})
	}
}

func TestApplyProposalShowsWhyCloudRefusedIt(t *testing.T) {
	for _, tt := range []struct {
		code, message, want string
	}{
		{
			"proposal_stale",
			"\"Add a weekly digest\" is stale because reading-list changed since it was opened",
			"error: \"Add a weekly digest\" is stale because reading-list changed since it was opened\n" +
				"To start over: telos pull goal_c7d2f0a4e8, then telos propose SPEC.md -t \"TITLE\"\n",
		},
		{"proposal_blocked", "admin@example.com blocked \"Add a weekly digest\"", "error: admin@example.com blocked \"Add a weekly digest\"\n"},
		{"approval_required", "\"Add a weekly digest\" needs an owner or admin to approve its latest update", "error: \"Add a weekly digest\" needs an owner or admin to approve its latest update\n"},
		{"proposal_closed", "\"Add a weekly digest\" is applying", "error: \"Add a weekly digest\" is applying\n"},
	} {
		t.Run(tt.code, func(t *testing.T) {
			proposalCLITest(t, map[string]http.HandlerFunc{
				"GET /api/proposals/p_3f9a2b7c":        respondJSON(testProposal("pending")),
				"POST /api/proposals/p_3f9a2b7c/apply": respondError(http.StatusConflict, tt.code, tt.message),
			})
			_, stderr, err := inferenceCLIProcess(t, "apply", "p_3f9a2b7c")
			if err == nil || stderr != tt.want {
				t.Fatalf("err=%v stderr=%q, want %q", err, stderr, tt.want)
			}
		})
	}
}
