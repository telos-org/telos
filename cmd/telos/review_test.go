package main

import (
	"net/http"
	"sync/atomic"
	"testing"
)

func TestReviewRecordsAVerdictOnTheLatestUpdate(t *testing.T) {
	for _, tt := range []struct {
		flag    string
		verdict any
		want    string
	}{
		{"--approved", "approved", "approved \"Add a weekly digest\"\n"},
		{"--blocked", "blocked", "blocked \"Add a weekly digest\"\n"},
		{"--none", nil, "cleared your review of \"Add a weekly digest\"\n"},
	} {
		t.Run(tt.flag, func(t *testing.T) {
			proposalCLITest(t, map[string]http.HandlerFunc{
				"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
				"PUT /api/proposals/p_3f9a2b7c/review": func(w http.ResponseWriter, r *http.Request) {
					if body := decodeBody(t, r); body["update_number"] != 2.0 || body["verdict"] != tt.verdict {
						t.Errorf("review body = %#v", body)
					}
					respondJSON(testProposal("pending"))(w, r)
				},
			})
			out := captureStdout(t, func() { cmdReview([]string{"p_3f9a2b7c", tt.flag}) })
			if out != tt.want {
				t.Fatalf("output = %q, want %q", out, tt.want)
			}
		})
	}
}

func TestReviewRecordsNothingWithoutExactlyOneVerdict(t *testing.T) {
	var reviews atomic.Int32
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
		"PUT /api/proposals/p_3f9a2b7c/review": func(w http.ResponseWriter, r *http.Request) {
			reviews.Add(1)
		},
	})
	for _, args := range [][]string{{"p_3f9a2b7c"}, {"p_3f9a2b7c", "--approved", "--blocked"}, {"p_3f9a2b7c", "--approved=false"}} {
		_, stderr, err := inferenceCLIProcess(t, append([]string{"review"}, args...)...)
		if want := "error: use one of --approved, --blocked, or --none\n"; err == nil || stderr != want || reviews.Load() != 0 {
			t.Fatalf("%v: err=%v reviews=%d stderr=%q, want %q", args, err, reviews.Load(), stderr, want)
		}
	}
}
