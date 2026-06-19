package executor

import (
	"fmt"
	"os"
	"strings"
)

type nativeProviderConfig struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
}

// providerDefaults describes a known provider's baked-in endpoint conventions.
// Every endpoint Telos talks to is OpenAI-compatible — either a provider's own
// OpenAI-style API or a LiteLLM proxy fronting it — so only the base URL and key
// environment variable vary.
type providerDefaults struct {
	baseURL string
	keyEnv  string
}

func nativeProviderRegistry() map[string]providerDefaults {
	return map[string]providerDefaults{
		"silares":       {"https://api.silares.com/v1", "SILARES_API_KEY"},
		"sail-research": {"https://api.sailresearch.com/v1", "SAIL_API_KEY"},
		"moonshot":      {"https://api.moonshot.ai/v1", "MOONSHOT_API_KEY"},
		"xai":           {"https://api.x.ai/v1", "XAI_API_KEY"},
		"openai":        {"https://api.openai.com/v1", "OPENAI_API_KEY"},
		"openai-codex":  {"https://api.openai.com/v1", "OPENAI_API_KEY"},
	}
}

func resolveNativeProvider(model string) (nativeProviderConfig, error) {
	if model == "" {
		return nativeProviderConfig{}, fmt.Errorf("model is required")
	}
	if base := firstEnv("TELOS_API_BASE_URL", "TELOS_BASE_URL"); base != "" {
		key := firstEnv("TELOS_API_KEY")
		if key == "" {
			return nativeProviderConfig{}, fmt.Errorf("TELOS_API_KEY is required when TELOS_API_BASE_URL is set")
		}
		return nativeProviderConfig{
			Provider: "telos",
			Model:    stripProviderPrefix(model),
			BaseURL:  strings.TrimRight(base, "/"),
			APIKey:   key,
		}, nil
	}

	provider, providerModel := splitProviderModel(model)
	registry := nativeProviderRegistry()
	if def, ok := registry[provider]; ok {
		return providerFromDefaults(provider, providerModel, def)
	}
	if provider != "" {
		envPrefix := providerEnvPrefix(provider)
		keyName := envPrefix + "_API_KEY"
		base := firstEnv(envPrefix + "_BASE_URL")
		key := firstEnv(keyName)
		if base == "" || key == "" {
			return nativeProviderConfig{}, fmt.Errorf("unknown provider %q; set %s_BASE_URL and %s", provider, envPrefix, keyName)
		}
		return nativeProviderConfig{
			Provider: provider,
			Model:    providerModel,
			BaseURL:  strings.TrimRight(base, "/"),
			APIKey:   key,
		}, nil
	}
	return providerFromDefaults("openai", model, registry["openai"])
}

func providerFromDefaults(provider, model string, def providerDefaults) (nativeProviderConfig, error) {
	envPrefix := providerEnvPrefix(provider)
	base := firstEnv(envPrefix + "_BASE_URL")
	if base == "" && provider == "openai-codex" {
		// openai-codex shares OpenAI's base URL when no codex-specific override is set.
		base = firstEnv("OPENAI_BASE_URL")
	}
	if base == "" {
		base = def.baseURL
	}
	key := firstEnv(def.keyEnv)
	if key == "" {
		return nativeProviderConfig{}, fmt.Errorf("%s is required for model provider %q", def.keyEnv, provider)
	}
	return nativeProviderConfig{
		Provider: provider,
		Model:    model,
		BaseURL:  strings.TrimRight(base, "/"),
		APIKey:   key,
	}, nil
}

func splitProviderModel(model string) (string, string) {
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 {
		return "", model
	}
	return parts[0], parts[1]
}

func stripProviderPrefix(model string) string {
	_, m := splitProviderModel(model)
	return m
}

func providerEnvPrefix(provider string) string {
	s := strings.ToUpper(provider)
	return strings.NewReplacer("-", "_", ".", "_").Replace(s)
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
