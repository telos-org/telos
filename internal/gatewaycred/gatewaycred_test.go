package gatewaycred

import "testing"

func TestNormalizeTransportAndKind(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		kind      string
		wantT     Transport
		wantK     Kind
	}{
		{name: "defaults openai", wantT: TransportOpenAISync, wantK: KindOpenAI},
		// Kind alone never opts into the async transport: BYO/env credentials
		// stay openai_sync unless transport is explicit; managed resolution
		// pre-fills bifrost_async before normalizing.
		{name: "bifrost kind keeps sync transport", kind: "bifrost", wantT: TransportOpenAISync, wantK: KindBifrost},
		{name: "async transport defaults bifrost kind", transport: "bifrost_async", wantT: TransportBifrostAsync, wantK: KindBifrost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotT, gotK, err := NormalizeTransportAndKind(tt.transport, tt.kind)
			if err != nil {
				t.Fatal(err)
			}
			if gotT != tt.wantT || gotK != tt.wantK {
				t.Fatalf("got transport=%q kind=%q, want %q/%q", gotT, gotK, tt.wantT, tt.wantK)
			}
		})
	}
}

func TestNormalizeProviderDefaultsAndProviderEnvKeys(t *testing.T) {
	t.Setenv(AnthropicAPIKeyEnv, "anthropic-env")
	cred, err := Normalize(Credential{Provider: ProviderAnthropic})
	if err != nil {
		t.Fatal(err)
	}
	if cred.Provider != ProviderAnthropic || cred.BaseURL != "https://api.anthropic.com" || cred.APIKey != "anthropic-env" {
		t.Fatalf("credential: %+v", cred)
	}
	if _, err := NormalizeProvider("bogus"); err == nil {
		t.Fatal("expected invalid provider error")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(BaseURLEnv, "https://gateway.example.com/openai/")
	t.Setenv(APIKeyEnv, " test-key ")
	t.Setenv(KindEnv, "bifrost")
	t.Setenv(HeadersEnv, `{" x-extra ":" ok "}`)
	t.Setenv(CostHardLimitEnv, "YES")

	cred, ok, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected env credential to be present")
	}
	if cred.BaseURL != "https://gateway.example.com/openai" || cred.APIKey != "test-key" {
		t.Fatalf("credential: %+v", cred)
	}
	if cred.Transport != TransportOpenAISync || cred.Kind != KindBifrost {
		t.Fatalf("transport/kind: %+v", cred)
	}
	if cred.Headers["x-extra"] != "ok" {
		t.Fatalf("headers: %#v", cred.Headers)
	}
	if !cred.CostHardLimit {
		t.Fatalf("cost hard limit should use shared YES grammar: %+v", cred)
	}
}

func TestFromEnvProviderFallbackAPIKey(t *testing.T) {
	t.Setenv(ProviderEnv, string(ProviderGemini))
	t.Setenv(GeminiAPIKeyEnv, "gemini-env")

	cred, ok, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || cred.Provider != ProviderGemini || cred.BaseURL != "https://generativelanguage.googleapis.com" || cred.APIKey != "gemini-env" {
		t.Fatalf("credential: ok=%v %+v", ok, cred)
	}
}

func TestFromEnvInvalidHeadersWithoutKeyIgnored(t *testing.T) {
	// Stale gateway env without a usable key must not opt the process into
	// gateway routing, even when the leftovers are malformed.
	t.Setenv(HeadersEnv, `["not-object"]`)
	if _, ok, err := FromEnv(); err != nil || ok {
		t.Fatalf("FromEnv stale invalid headers: ok=%v err=%v", ok, err)
	}
	if !EnvPresent() {
		t.Fatal("EnvPresent should still report the stale variable")
	}
}

func TestFromEnvInvalidHeadersWithKeyError(t *testing.T) {
	t.Setenv(APIKeyEnv, "test-key")
	t.Setenv(HeadersEnv, `["not-object"]`)
	if _, ok, err := FromEnv(); err == nil || !ok {
		t.Fatalf("FromEnv invalid headers with key: ok=%v err=%v", ok, err)
	}
}

func TestFromEnvStaleBaseURLWithoutKeyIgnored(t *testing.T) {
	t.Setenv(BaseURLEnv, "https://stale-gateway.example.com/v1")
	if _, ok, err := FromEnv(); err != nil || ok {
		t.Fatalf("FromEnv stale base URL: ok=%v err=%v", ok, err)
	}
}

func TestCostHardLimitBillingBackedEnv(t *testing.T) {
	t.Setenv(BillingEnvIDEnv, "env_test")
	t.Setenv(BillingTokenEnv, "billing-token")
	if !CostHardLimitFromEnv() {
		t.Fatal("billing-backed env should hard-enforce unknown cost")
	}
}

func TestNormalizeWithEnvPolicyAppliesCostHardLimit(t *testing.T) {
	t.Setenv(CostHardLimitEnv, "YES")
	cred, err := NormalizeWithEnvPolicy(Credential{
		BaseURL: "https://gateway.example.com/v1",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cred.CostHardLimit {
		t.Fatalf("env cost policy should apply to normalized credential: %+v", cred)
	}
}
