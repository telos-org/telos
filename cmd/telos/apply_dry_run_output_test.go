package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestDryRunSettingsTextOnlyShowsChangedValues(t *testing.T) {
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"GET /api/deployments/goal_test/inference": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"selection":{"source":"managed","tier":"default"},"inference":{"source":"managed","tier":"default"},"agent_model":"telos-bifrost/telos/default","agent_thinking":"high"}`))
		},
	})
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	for _, tt := range []struct {
		name, operation, model, thinking string
		args                             []string
		inherited                        bool
	}{
		{"thinking only", "update", "telos/default (Goal setting)", "high -> off", []string{"--thinking", "off"}, false},
		{"model only", "update", "telos/default -> telos/max", "high (Goal setting)", []string{"--model", "telos/max"}, true},
		{"unchanged", "unchanged", "telos/default", "high", []string{"--model", "telos/default", "--thinking", "high"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--goal", "goal_test", "--dry-run"}, tt.args...)
			output := captureStdout(t, func() { cmdApply(args) })
			lines := []string{
				"Operation " + tt.operation,
				"Context   personal",
				"Goal      goal_test",
				"Model     " + tt.model,
				"Thinking  " + tt.thinking,
				"",
				"Other server-side checks run when applying.",
			}
			if tt.inherited {
				lines = append(lines, "The server resolves inherited thinking for the selected model when applying.")
			}
			lines = append(lines, "Nothing was applied.", "")
			if want := strings.Join(lines, "\n"); output != want {
				t.Fatalf("settings preview:\n%s\nwant:\n%s", output, want)
			}
		})
	}
}

func TestDryRunCreateSkillDiffUsesAnEmptyBaseline(t *testing.T) {
	fixture := makeDryRunSkillFixture(t)
	handlers := registryApplyHandlers(fixture.pkg)
	handlers["GET /api/skills/telos/verify-test/versions/1.0.0"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.SkillRecord{Digest: fixture.digest})
	}
	handlers["GET /api/skills/telos/verify-test/versions/1.0.0/bundle"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture.bundle)
	}
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	output := captureStdout(t, func() {
		cmdApply([]string{"@telos/demo:1.2.3", "--dry-run", "--diff"})
	})
	for _, expected := range []string{
		"added skills/verify-test/SKILL.md\n",
		"--- /dev/null\n",
		"+++ proposed/skills/verify-test/SKILL.md\n",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "deployed/") || strings.Contains(output, "mode - ->") {
		t.Fatalf("create preview invented deployed state:\n%s", output)
	}
}

func TestDryRunFileSummariesOnlyShowActualModeChanges(t *testing.T) {
	before := map[string]cloud.SkillFile{
		"removed": {Mode: "0644", Data: []byte("old")},
		"script":  {Mode: "0644", Data: []byte("run")},
	}
	after := map[string]cloud.SkillFile{
		"added":  {Mode: "0644", Data: []byte("new")},
		"script": {Mode: "0755", Data: []byte("run")},
	}
	var output bytes.Buffer
	printApplyPreview(&output, &applyPreview{SkillDiffs: diffSkillFiles("test", before, after)}, false)
	for _, expected := range []string{
		"added skills/test/added\n",
		"removed skills/test/removed\n",
		"modified skills/test/script (mode 0644 -> 0755)\n",
		"+++ /dev/null\n",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q:\n%s", expected, output.String())
		}
	}
}

func TestDryRunUpdateNotesDoNotRepeatSnapshotCheck(t *testing.T) {
	server := inferenceTestServer(t, registryApplyHandlers(testApplyPackage(t)))
	defer server.Close()
	configureDryRunTest(t, server.URL)
	preview := decodeApplyPreview(t, "@telos/demo:1.2.3", "--goal", "goal_test")
	if len(preview.Notes) != 1 || strings.Contains(preview.Notes[0], "snapshot") {
		t.Fatalf("redundant update notes: %v", preview.Notes)
	}
}
