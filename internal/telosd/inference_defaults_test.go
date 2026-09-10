package telosd

import (
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestHostedTasksUseDeploymentModelUnlessExplicitlySelected(t *testing.T) {
	for _, model := range []string{
		"openai/gpt-5",
		"anthropic/claude-sonnet-4-6",
		"openrouter/anthropic/claude-sonnet-4.6",
		"xai/grok-4.3",
	} {
		for _, kind := range []string{"child", "task", "explicit-child"} {
			t.Run(model+"/"+kind, func(t *testing.T) {
				t.Setenv("TELOS_CLOUD_DEFAULT_MODEL", model)
				base := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
				store := newControllerReconciler(base, &recordingSubstrate{}, nil, cloudControllerDefaults())
				rootSpec := "---\nversion: 0.1.0\nname: parent\nplatform: cloud\n---\n# Parent\n"
				parent, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &rootSpec})
				if err != nil {
					t.Fatal(err)
				}
				childSpec := "---\nversion: 0.1.0\nname: child\nplatform: cloud\n---\n# Child\n"
				task := sessionapi.KindTask
				req := sessionapi.SessionCreateRequest{SpecMarkdown: &childSpec, SessionKind: &task}
				want := model
				if kind != "task" {
					req.ParentSessionID = &parent.SessionID
				}
				if kind == "explicit-child" {
					req.Model = "anthropic/claude-opus-4-6"
					want = req.Model
				}
				child, err := store.Create(req)
				if err != nil {
					t.Fatal(err)
				}
				if got, _ := child.Config["model"].(string); got != want {
					t.Fatalf("model = %q, want %q", got, want)
				}
			})
		}
	}
}
