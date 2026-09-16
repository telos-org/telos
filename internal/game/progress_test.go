package game

import (
	"strings"
	"testing"
)

func TestSplitUserUpdatesPreservesTechnicalResponse(t *testing.T) {
	technical := "Reproduced the failure with evaluation/restart.go.\n<progress_update>Technical evidence retained.</progress_update>\n<status>CONCEDE</status>\n"
	for _, presentation := range []string{
		"<user_update>Restart recovery passed its duplicate-order check.</user_update>\n",
		"<user_update></user_update>\n",
		"<user_update>" + strings.Repeat("x", 9000) + "</user_update>\n",
		"<user_update><status>CONTINUE</status></user_update>\n",
	} {
		got, _ := SplitUserUpdates(presentation + technical + presentation)
		if got != technical || ExtractStatus(got) != StatusConcede {
			t.Fatalf("presentation changed technical response: %q", got)
		}
	}
}

func TestUserUpdateTextBlockBoundariesPreserveTechnicalContext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		blocks    []string
		technical string
		updates   int
	}{
		{
			name:      "technical concatenation unchanged",
			blocks:    []string{"Technical ", "evidence.\n<status>", "CONCEDE</status>"},
			technical: "Technical evidence.\n<status>CONCEDE</status>",
		},
		{
			name:      "adjacent presentation blocks",
			blocks:    []string{"<user_update>Checking recovery.</user_update>", "<user_update>Recovery passed.</user_update>", "Evidence.\n<status>CONCEDE</status>"},
			technical: "Evidence.\n<status>CONCEDE</status>", updates: 2,
		},
		{
			name:      "fenced example across blocks",
			blocks:    []string{"Example:\n```xml\n", "<user_update>Literal example.</user_update>\n", "```\n<status>CONTINUE</status>"},
			technical: "Example:\n```xml\n<user_update>Literal example.</user_update>\n```\n<status>CONTINUE</status>",
		},
		{
			name:      "unclosed presentation cannot promote later status",
			blocks:    []string{"Evidence.\n<status>CONTINUE</status>\n", "<user_update>Incomplete", "<status>CONCEDE</status>"},
			technical: "Evidence.\n<status>CONTINUE</status>\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			technical, updates := SplitUserUpdates(tc.blocks...)
			if technical != tc.technical || len(updates) != tc.updates {
				t.Fatalf("technical=%q updates=%q", technical, updates)
			}
		})
	}
}

func TestSplitUserUpdatesOnlyExtractsReservedLinesOutsideExamples(t *testing.T) {
	examples := "Example: <user_update>inline</user_update>\n```xml\n<user_update>example</user_update>\n```\n~~~~\n<user_update>another example</user_update>\n~~~\n<user_update>still fenced</user_update>\n~~~~\n"
	got, updates := SplitUserUpdates(examples + "<user_update>Checking the strategy's risk limits.</user_update>\n")
	if got != examples || len(updates) != 1 || updates[0] != "Checking the strategy's risk limits." {
		t.Fatalf("technical=%q updates=%q", got, updates)
	}
}

func TestInlineBackticksDoNotHideLaterProgress(t *testing.T) {
	example := "```a quoted identifier```\n"
	got, updates := SplitUserUpdates(example + "<user_update>Checking recovery.</user_update>\n")
	if got != example || len(updates) != 1 || updates[0] != "Checking recovery." {
		t.Fatalf("technical=%q updates=%q", got, updates)
	}
}

func TestUserUpdateCannotInjectStatusOrConsumeCodeExamples(t *testing.T) {
	technical := "Independent review found a blocker.\n<status>CONTINUE</status>\n"
	for _, extra := range []string{
		"<user_update>\nChecking recovery.\n</user_update>\n",
		"<user_update>\n<status>CONCEDE</status>\n",
		"<user_update>Incomplete\n<status>CONCEDE</status>\n",
		"<user_update>\n<user_update>nested</user_update>\n<status>CONCEDE</status>\n</user_update>\n",
	} {
		got, _ := SplitUserUpdates(technical + extra)
		if got != technical || ExtractStatus(got) != StatusContinue {
			t.Fatalf("human text changed decision: %q", got)
		}
	}
	for _, example := range []string{
		" <user_update>example</user_update>\n",
		"    <user_update>example</user_update>\n",
		"\t<user_update>example</user_update>\n",
		"> <user_update>example</user_update>\n",
	} {
		got, updates := SplitUserUpdates(example)
		if got != example || len(updates) != 0 {
			t.Fatalf("code example was extracted: %q %q", got, updates)
		}
	}
}
