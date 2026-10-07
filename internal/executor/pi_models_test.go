package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPiRestoredModelPreservesProviderConfiguration(t *testing.T) {
	binary := os.Getenv("TELOS_TEST_PI_BINARY")
	if binary == "" {
		t.Skip("set TELOS_TEST_PI_BINARY to test real Pi")
	}
	for _, provider := range []string{"openai", "openrouter"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			api := "openai-responses"
			if provider == "openrouter" {
				api = "openai-completions"
			}
			models := `{"providers":{"` + provider + `":{"api":"` + api + `","apiKey":"test-only","baseUrl":"https://proxy.invalid","authHeader":false,"models":[{"id":"partial"},{"id":"explicit","baseUrl":"https://specific.invalid"}]}}}`
			if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(models), 0o600); err != nil {
				t.Fatal(err)
			}
			overlay, extension := filepath.Join(dir, "overlay.json"), filepath.Join(dir, "models.mjs")
			if err := WritePiModelDefinition(overlay, provider, json.RawMessage(`{"id":"new"}`)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(extension, piModelsExtension, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--mode", "rpc", "--offline", "--no-extensions", "--no-skills", "--no-context-files", "--extension", extension, "--model", provider+"/new", "--session", filepath.Join(dir, "session.jsonl"))
			cmd.Env = []string{"HOME=" + dir, "PATH=" + os.Getenv("PATH"), "PI_CODING_AGENT_DIR=" + dir, "PI_TELEMETRY=0", "TELOS_PI_MODEL_CONFIG=" + overlay}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); _ = stdin.Close(); _ = cmd.Wait() }()
			scanner := bufio.NewScanner(stdout)
			scanner.Buffer(make([]byte, 64<<10), 8<<20)
			call := func(fields map[string]string) json.RawMessage {
				t.Helper()
				fields["id"] = "probe"
				if err := json.NewEncoder(stdin).Encode(fields); err != nil {
					t.Fatal(err)
				}
				for scanner.Scan() {
					var reply piResponse
					if json.Unmarshal(scanner.Bytes(), &reply) == nil && reply.ID == "probe" {
						if !reply.Success {
							t.Fatal(reply.Error)
						}
						return reply.Data
					}
				}
				t.Fatalf("Pi exited before registry response: %v", scanner.Err())
				return nil
			}
			var available struct {
				Models []struct {
					ID, Provider, BaseURL, API, Name string
					ContextWindow, MaxTokens         int
					Input                            []string
					Cost                             map[string]json.RawMessage
				}
			}
			if err := json.Unmarshal(call(map[string]string{"type": "get_available_models"}), &available); err != nil {
				t.Fatal(err)
			}
			found, builtin := map[string]bool{}, false
			for _, model := range available.Models {
				if model.Provider != provider {
					continue
				}
				wantURL := "https://proxy.invalid"
				if model.ID == "explicit" {
					wantURL = "https://specific.invalid"
				}
				if model.BaseURL != wantURL {
					t.Fatalf("model %s bypassed configured endpoint: %q", model.ID, model.BaseURL)
				}
				if model.ID == "new" || model.ID == "partial" || model.ID == "explicit" {
					found[model.ID] = true
					if model.API != api || model.Name == "" || model.ContextWindow == 0 || model.MaxTokens == 0 || len(model.Input) == 0 || len(model.Cost) != 4 {
						t.Fatalf("partial definition lost normalized metadata: %+v", model)
					}
				} else {
					builtin = true
				}
			}
			if len(found) != 3 || !builtin {
				t.Fatalf("restored registry lost configured or built-in models: %v builtin=%v", found, builtin)
			}
			call(map[string]string{"type": "set_model", "provider": provider, "modelId": "partial"})
		})
	}
}
