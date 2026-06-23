package game

import (
	"reflect"
	"testing"
)

func TestParseReviewBlock(t *testing.T) {
	t.Run("well-formed CSV", func(t *testing.T) {
		text := "<review>criteria,score\nclarity,8.0/10\ncorrectness,4.5/10</review>\n<summary>fine</summary>"
		criteria, ok := ParseReviewBlock(text)
		if !ok {
			t.Fatal("expected ok")
		}
		want := []ReviewCriterion{
			{Name: "clarity", Score: 8.0, Max: 10, Raw: "8.0/10"},
			{Name: "correctness", Score: 4.5, Max: 10, Raw: "4.5/10"},
		}
		if !reflect.DeepEqual(criteria, want) {
			t.Fatalf("criteria: got %+v want %+v", criteria, want)
		}
	})

	t.Run("criteria text with comma splits on last comma", func(t *testing.T) {
		criteria, ok := ParseReviewBlock("<review>criteria,score\nhandles input, output, and errors,6/10</review>")
		if !ok || len(criteria) != 1 {
			t.Fatalf("got ok=%t criteria=%+v", ok, criteria)
		}
		if criteria[0].Name != "handles input, output, and errors" || criteria[0].Score != 6 {
			t.Fatalf("criterion: %+v", criteria[0])
		}
	})

	t.Run("last review block wins", func(t *testing.T) {
		text := "<review>criteria,score\na,1/10</review>\n<review>criteria,score\nb,9/10</review>"
		criteria, ok := ParseReviewBlock(text)
		if !ok || len(criteria) != 1 || criteria[0].Name != "b" {
			t.Fatalf("expected last block (b), got ok=%t %+v", ok, criteria)
		}
	})

	t.Run("garbage rows skipped not counted", func(t *testing.T) {
		text := "<review>criteria,score\nclarity,8/10\nthis row has no score\ncorrectness,3/10</review>"
		criteria, ok := ParseReviewBlock(text)
		if !ok || len(criteria) != 2 {
			t.Fatalf("expected 2 valid rows, got ok=%t %+v", ok, criteria)
		}
	})

	t.Run("no block or no rows", func(t *testing.T) {
		if _, ok := ParseReviewBlock("no review here"); ok {
			t.Fatal("expected ok=false with no block")
		}
		if _, ok := ParseReviewBlock("<review>criteria,score\n</review>"); ok {
			t.Fatal("expected ok=false with header only")
		}
	})
}

func TestFindingsFromReview(t *testing.T) {
	criteria, ok := ParseReviewBlock("<review>criteria,score\nclarity,8/10\ncorrectness,4/10\ncoverage,7/10</review>")
	if !ok {
		t.Fatal("expected ok")
	}
	findings := findingsFromReview(criteria, defaultFindingScoreThreshold)
	want := []string{"correctness (4/10)"}
	if !reflect.DeepEqual(findings, want) {
		t.Fatalf("findings: got %v want %v (threshold flags <7/10; 7/10 passes)", findings, want)
	}

	allPass, _ := ParseReviewBlock("<review>criteria,score\nclarity,8/10\ncorrectness,9/10</review>")
	if got := findingsFromReview(allPass, defaultFindingScoreThreshold); len(got) != 0 {
		t.Fatalf("all-pass review should yield no findings, got %v", got)
	}
}

func TestParseFindingsBlock(t *testing.T) {
	t.Run("severity and description", func(t *testing.T) {
		text := "Findings below.\n<findings>blocker | missing error handling\nwarn | naming could improve</findings>\n<status>CONTINUE</status>"
		findings, ok := ParseFindingsBlock(text)
		if !ok {
			t.Fatal("expected ok")
		}
		want := []Finding{
			{Severity: "blocker", Text: "missing error handling"},
			{Severity: "warn", Text: "naming could improve"},
		}
		if !reflect.DeepEqual(findings, want) {
			t.Fatalf("findings: got %+v want %+v", findings, want)
		}
	})

	t.Run("line without pipe defaults to blocker", func(t *testing.T) {
		findings, ok := ParseFindingsBlock("<findings>the API contract is violated</findings>")
		if !ok || len(findings) != 1 || findings[0].Severity != "blocker" {
			t.Fatalf("got ok=%t %+v", ok, findings)
		}
	})

	t.Run("empty block is present but yields no findings", func(t *testing.T) {
		findings, ok := ParseFindingsBlock("<findings></findings>\n<status>CONCEDE</status>")
		if !ok {
			t.Fatal("empty block should still report ok=true")
		}
		if len(findings) != 0 {
			t.Fatalf("expected no findings, got %+v", findings)
		}
	})

	t.Run("no block", func(t *testing.T) {
		if _, ok := ParseFindingsBlock("prose with no findings tag"); ok {
			t.Fatal("expected ok=false with no block")
		}
	})
}

func TestFindingsBlockPrecedenceOverReviewAndKeywords(t *testing.T) {
	// A <findings> block wins over a co-present <review> CSV.
	text := "<review>criteria,score\ncorrectness,2/10</review>\n<findings>blocker | concrete failure</findings>"
	findings, ok := findingsBlockFindings(text)
	if !ok || len(findings) != 1 || findings[0] != "blocker: concrete failure" {
		t.Fatalf("findings block should win: ok=%t %v", ok, findings)
	}
	// With no findings block, the caller falls through to the review CSV.
	if _, ok := findingsBlockFindings("<review>criteria,score\ncorrectness,2/10</review>"); ok {
		t.Fatal("expected findingsBlockFindings ok=false when no <findings> block")
	}
}

func TestReviewFindingsFallsBackOnNonReviewText(t *testing.T) {
	// pvg-mode verifier prose has no <review> block, so reviewFindings declines
	// and the caller keeps using the legacy keyword scan.
	if _, ok := reviewFindings("The implementation is missing error handling.\n<status>CONTINUE</status>"); ok {
		t.Fatal("expected reviewFindings ok=false for non-review text")
	}
	if findings, ok := reviewFindings("<review>criteria,score\ncorrectness,2/10</review>"); !ok || len(findings) != 1 {
		t.Fatalf("expected one review finding, got ok=%t %v", ok, findings)
	}
}
