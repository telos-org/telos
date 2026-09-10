package sessionworker

import (
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestWorkerExportsSelectedModelForNestedCLI(t *testing.T) {
	for _, model := range []string{
		"openai/gpt-5",
		"anthropic/claude-sonnet-4-6",
		"openrouter/anthropic/claude-sonnet-4.6",
		"xai/grok-4.3",
	} {
		t.Run(model, func(t *testing.T) {
			t.Setenv("TELOS_MODEL", "openai-codex/gpt-5.5")
			sessionDir := t.TempDir()
			manifest := &sessionapi.Manifest{
				SessionKind: sessionapi.KindController,
				Runtime:     sessionapi.RuntimeCloud,
			}
			manifest.Config.Model = model
			if err := sessionapi.WriteManifest(manifestPath(sessionDir), manifest); err != nil {
				t.Fatal(err)
			}
			environment := map[string]string{}
			for _, entry := range Env(sessionDir, StartOptions{Runtime: sessionapi.RuntimeCloud}) {
				key, value, _ := strings.Cut(entry, "=")
				environment[key] = value
			}
			if got := environment["TELOS_MODEL"]; got != model {
				t.Fatalf("nested CLI model = %q, want %q", got, model)
			}
		})
	}
}
