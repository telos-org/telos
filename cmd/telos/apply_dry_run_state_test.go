package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/spec"
)

func TestPreviewSpecStateCapturesResolvedChanges(t *testing.T) {
	currentIntervalSpec := []byte("---\nname: demo\nversion: 1.0.0\ninterval: 5m\n---\n\n# Goal\n\nCurrent.\n")
	proposedIntervalSpec := []byte("---\nname: demo\nversion: 1.1.0\ninterval: 10m\n---\n\n# Goal\n\nProposed.\n")
	currentManifest := &spec.ApplyPackageManifest{Skills: map[string]spec.ApplyPackageSkillLock{
		"verify-quality": {
			Ref:    "@telos/verify-quality:1.0.0",
			Digest: "sha256:current",
		},
	}}
	proposedManifest := &spec.ApplyPackageManifest{Skills: map[string]spec.ApplyPackageSkillLock{
		"verify-quality": {
			Ref:     "@telos/verify-quality:1.1.0",
			Digest:  "sha256:proposed",
			Starred: true,
		},
	}}

	current, err := previewSpecStateFromMarkdown(currentIntervalSpec, currentManifest)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := previewSpecStateFromMarkdown(proposedIntervalSpec, proposedManifest)
	if err != nil {
		t.Fatal(err)
	}
	if current.IntervalSeconds == nil || *current.IntervalSeconds != 300 {
		t.Fatalf("current interval = %#v", current.IntervalSeconds)
	}
	if proposed.IntervalSeconds == nil || *proposed.IntervalSeconds != 600 {
		t.Fatalf("proposed interval = %#v", proposed.IntervalSeconds)
	}
	if len(proposed.Skills) != 1 || !proposed.Skills[0].Starred {
		t.Fatalf("proposed required skill = %#v", proposed.Skills)
	}

	var out bytes.Buffer
	printPreviewStateDelta(&out, current, proposed)
	normalized := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"Version 1.0.0 -> 1.1.0",
		"Interval 5m -> 10m",
		"verify-quality @telos/verify-quality:1.0.0 sha256:current",
		"verify-quality @telos/verify-quality:1.1.0 sha256:proposed *",
	} {
		want = strings.Join(strings.Fields(want), " ")
		if !strings.Contains(normalized, want) {
			t.Fatalf("plan delta missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(normalized, "rubrics") {
		t.Fatalf("starred skill locks already express required rubrics:\n%s", out.String())
	}
}

func TestPrintPreviewStateDeltaOmitsUnchangedResolvedState(t *testing.T) {
	interval := 300
	state := previewSpecState{
		Version:         "1.0.0",
		IntervalSeconds: &interval,
		Skills: []previewSkillLock{{
			Name:    "verify-quality",
			Ref:     "@telos/verify-quality:1.0.0",
			Digest:  "sha256:same",
			Starred: true,
		}},
	}

	var out bytes.Buffer
	printPreviewStateDelta(&out, state, state)
	if out.Len() != 0 {
		t.Fatalf("unchanged resolved state should be silent:\n%s", out.String())
	}
}

func TestFormatPreviewIntervalMatchesSpecStyle(t *testing.T) {
	for seconds, want := range map[int]string{45: "45s", 30 * 60: "30m", 6 * 60 * 60: "6h", 90 * 60: "1h30m", 60*60 + 30: "1h0m30s"} {
		if got := formatPreviewInterval(&seconds); got != want {
			t.Fatalf("formatPreviewInterval(%d) = %q, want %q", seconds, got, want)
		}
	}
	if got := formatPreviewInterval(nil); got != "-" {
		t.Fatalf("formatPreviewInterval(nil) = %q", got)
	}
}
