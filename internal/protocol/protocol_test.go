package protocol

import "testing"

func TestParseFinalStatus(t *testing.T) {
	status, ok := ParseFinalStatus("done\n<status data-x=\"1\">CONCEDE</status>\n")
	if !ok || status != StatusConcede {
		t.Fatalf("ParseFinalStatus: got %q ok=%v", status, ok)
	}
	if _, ok := ParseFinalStatus("<status>CONCEDE</status>\ntrailing prose"); ok {
		t.Fatal("non-final status should not parse")
	}
}

func TestBlockExtractionToleratesAttributes(t *testing.T) {
	text := `<progress_update data-round="1">implemented</progress_update>`
	if CountBlocks(TagProgressUpdate, text) != 1 {
		t.Fatalf("count progress blocks")
	}
	if got := LastProgressUpdate(text); got != "implemented" {
		t.Fatalf("progress: got %q", got)
	}
}

func TestParseReviewBlock(t *testing.T) {
	criteria, ok := ParseReviewBlock("<review>criteria,score\nhandles commas, in name,6/10</review>")
	if !ok || len(criteria) != 1 || criteria[0].Name != "handles commas, in name" || criteria[0].Score != 6 {
		t.Fatalf("criteria: ok=%v %#v", ok, criteria)
	}
}

func TestParseFindingsBlock(t *testing.T) {
	findings, ok := ParseFindingsBlock("<findings>warn | improve tests\nbare finding</findings>")
	if !ok || len(findings) != 2 || findings[0].Severity != "warn" || findings[1].Severity != "blocker" {
		t.Fatalf("findings: ok=%v %#v", ok, findings)
	}
}
