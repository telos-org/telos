package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/spec"
)

func TestCommentAnchorsToALineOrFieldItQuotes(t *testing.T) {
	pkg := testApplyPackage(t)
	added := "- Every Friday, email each reader a digest of new books."
	for _, tt := range []struct {
		name   string
		args   []string
		anchor map[string]any
		want   string
	}{
		{
			name:   "proposal line",
			args:   []string{"p_3f9a2b7c", "--file", "SPEC.md", "--line", "10"},
			anchor: map[string]any{"path": "SPEC.md", "quote": added, "prefix": lastRunes(strings.TrimSuffix(testProposedSpec, added+"\n"), 64), "suffix": "\n", "side": "new"},
			want:   "commented on SPEC.md line 10 (c_4d7a1f0e)\n",
		},
		{
			name:   "removed line",
			args:   []string{"p_3f9a2b7c", "--line", "7", "--old"},
			anchor: map[string]any{"path": "SPEC.md", "quote": "Keep a reading list.", "prefix": "---\nname: reading-list\nversion: 0.1.0\ninterval: 6h\n---\n# Goal\n", "suffix": "\n# Acceptance\n- Books remain available when the application rest", "side": "old"},
			want:   "commented on SPEC.md line 7 (old) (c_4d7a1f0e)\n",
		},
		{
			name:   "proposal field",
			args:   []string{"p_3f9a2b7c", "--field", "interval"},
			anchor: map[string]any{"path": "SPEC.md", "field": "interval"},
			want:   "commented on SPEC.md interval (c_4d7a1f0e)\n",
		},
		{
			name: "whole proposal",
			args: []string{"p_3f9a2b7c"},
			want: "commented on p_3f9a2b7c (c_4d7a1f0e)\n",
		},
		{
			name:   "Goal line",
			args:   []string{"goal_c7d2f0a4e8", "--line", "9"},
			anchor: map[string]any{"path": "SPEC.md", "quote": "Serve a demo.", "prefix": "---\nname: demo\nversion: 1.2.3\nplatform: cloud\n---\n\n# Goal\n\n", "suffix": "\n", "side": "new"},
			want:   "commented on SPEC.md line 9 (c_4d7a1f0e)\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			proposalCLITest(t, map[string]http.HandlerFunc{
				"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
				"GET /api/deployments/goal_c7d2f0a4e8": respondJSON(map[string]any{
					"id": "goal_c7d2f0a4e8", "package_ref": "@telos/demo:1.2.3", "package_digest": pkg.Digest,
				}),
				"GET /api/packages/telos/demo/versions/1.2.3/bundle": func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write(pkg.Bytes)
				},
				"POST /api/comments": func(w http.ResponseWriter, r *http.Request) {
					body = decodeBody(t, r)
					respondJSON(map[string]any{"id": "c_4d7a1f0e"})(w, r)
				},
			})

			out := captureStdout(t, func() { cmdComment(append(tt.args, "-m", "Send it on Mondays.")) })
			if out != tt.want {
				t.Fatalf("output = %q, want %q", out, tt.want)
			}
			if body["body"] != "Send it on Mondays." || !reflect.DeepEqual(body["anchor"], anyOrNil(tt.anchor)) {
				t.Fatalf("comment = %#v\nwant anchor %#v", body, tt.anchor)
			}
			if strings.HasPrefix(tt.args[0], "p_") && (body["proposal"] != "p_3f9a2b7c" || body["update_number"] != 2.0) {
				t.Fatalf("proposal comment = %#v", body)
			}
		})
	}
}

// anyOrNil returns a nil map as the nil interface, as JSON null decodes.
func anyOrNil(anchor map[string]any) any {
	if anchor == nil {
		return nil
	}
	return anchor
}

func TestCommentChangesAThread(t *testing.T) {
	for _, tt := range []struct {
		args  []string
		route string
		body  map[string]any
		want  string
	}{
		{[]string{"reply", "-m", "Done."}, "POST /api/comments/c_4d7a1f0e/replies", map[string]any{"body": "Done."}, "replied to c_4d7a1f0e\n"},
		{[]string{"edit", "-m", "Send it on Fridays."}, "PATCH /api/comments/c_4d7a1f0e", map[string]any{"body": "Send it on Fridays."}, "edited c_4d7a1f0e\n"},
		{[]string{"resolve"}, "PATCH /api/comments/c_4d7a1f0e", map[string]any{"resolved": true}, "resolved c_4d7a1f0e\n"},
		{[]string{"reopen"}, "PATCH /api/comments/c_4d7a1f0e", map[string]any{"resolved": false}, "reopened c_4d7a1f0e\n"},
		{[]string{"delete"}, "DELETE /api/comments/c_4d7a1f0e", nil, "deleted c_4d7a1f0e\n"},
	} {
		t.Run(tt.args[0], func(t *testing.T) {
			proposalCLITest(t, map[string]http.HandlerFunc{
				tt.route: func(w http.ResponseWriter, r *http.Request) {
					if tt.body != nil && !reflect.DeepEqual(decodeBody(t, r), tt.body) {
						t.Errorf("%s body differs from %#v", tt.route, tt.body)
					}
					w.WriteHeader(http.StatusNoContent)
				},
			})
			out := captureStdout(t, func() { cmdComment(append([]string{"c_4d7a1f0e"}, tt.args...)) })
			if out != tt.want {
				t.Fatalf("output = %q, want %q", out, tt.want)
			}
		})
	}
}

func TestCommentRefusesMisuseBeforeCallingCloud(t *testing.T) {
	proposalCLITest(t, nil)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"goal_c7d2f0a4e8", "-m", "Hi"}, "error: a comment on a Goal requires --file and --line, or --field; only proposals take comments on the whole thing\n"},
		{[]string{"goal_c7d2f0a4e8", "-m", "Hi", "--line", "3", "--old"}, "error: --old applies only to a proposal\n"},
		{[]string{"p_3f9a2b7c", "-m", "Hi", "--line", "3", "--field", "interval"}, "error: use --file and --line, or --field, not both\n"},
		{[]string{"p_3f9a2b7c", "--line", "3"}, "error: -m TEXT is required\n"},
		{[]string{"c_4d7a1f0e", "reply"}, "error: reply requires -m TEXT\n"},
		{[]string{"c_4d7a1f0e", "resolve", "-m", "Hi"}, "error: resolve doesn't take -m\n"},
		{[]string{"c_4d7a1f0e", "approve"}, "error: unknown action \"approve\"; use reply, edit, resolve, reopen, or delete\n"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			_, stderr, err := inferenceCLIProcess(t, append([]string{"comment"}, tt.args...)...)
			if err == nil || stderr != tt.want {
				t.Fatalf("err=%v stderr=%q, want %q", err, stderr, tt.want)
			}
		})
	}
}

func TestCommentOnTheOldSideTakesOnlyARemovedLine(t *testing.T) {
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
	})
	_, stderr, err := inferenceCLIProcess(t, "comment", "p_3f9a2b7c", "-m", "Hi", "--line", "8", "--old")
	want := "error: SPEC.md line 8 isn't a line the proposal removes; --old takes a removed line's number from telos describe\n"
	if err == nil || stderr != want {
		t.Fatalf("err=%v stderr=%q, want %q", err, stderr, want)
	}
}

func TestLineAnchorQuotesALongLineUpToCloudsLimit(t *testing.T) {
	long := strings.Repeat("a", maxQuote+500)
	anchor, err := lineAnchor(map[string]string{"SPEC.md": "# Goal\n" + long + "\nEnd.\n"}, "SPEC.md", 2)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Quote != long[:maxQuote] || anchor.Prefix != "# Goal\n" || anchor.Suffix != long[:64] {
		t.Fatalf("anchor quote %d chars, prefix %q, suffix %q", len(anchor.Quote), anchor.Prefix, anchor.Suffix)
	}
	if _, err := lineAnchor(map[string]string{"SPEC.md": "# Goal\n\n"}, "SPEC.md", 2); err == nil ||
		err.Error() != "SPEC.md line 2 is blank; comment on a line with text" {
		t.Fatalf("blank line error = %v", err)
	}
}

func TestCommentOnAFileTheProposalLeavesUnchangedReadsItsBundle(t *testing.T) {
	compiled := testSkillSpec(t)
	pkg, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		t.Fatal(err)
	}
	var anchor any
	proposalCLITest(t, map[string]http.HandlerFunc{
		"GET /api/proposals/p_3f9a2b7c": respondJSON(testProposal("pending")),
		"GET /api/proposals/p_3f9a2b7c/bundle": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Telos-Package-Digest", pkg.Digest)
			_, _ = w.Write(pkg.Bytes)
		},
		"POST /api/comments": func(w http.ResponseWriter, r *http.Request) {
			anchor = decodeBody(t, r)["anchor"]
			respondJSON(map[string]any{"id": "c_4d7a1f0e"})(w, r)
		},
	})
	out := captureStdout(t, func() {
		cmdComment([]string{"p_3f9a2b7c", "-m", "Hi", "--file", "skills/notes/SKILL.md", "--line", "5"})
	})
	quote, _ := anchor.(map[string]any)["quote"].(string)
	if out != "commented on skills/notes/SKILL.md line 5 (c_4d7a1f0e)\n" || quote != "Keep notes short." {
		t.Fatalf("output = %q, anchor = %#v", out, anchor)
	}
}
