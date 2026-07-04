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
		{name: "bifrost kind defaults async transport", kind: "bifrost", wantT: TransportBifrostAsync, wantK: KindBifrost},
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
	if cred.Transport != TransportBifrostAsync || cred.Kind != KindBifrost {
		t.Fatalf("transport/kind: %+v", cred)
	}
	if cred.Headers["x-extra"] != "ok" {
		t.Fatalf("headers: %#v", cred.Headers)
	}
	if !cred.CostHardLimit {
		t.Fatalf("cost hard limit should use shared YES grammar: %+v", cred)
	}
}

func TestFromEnvInvalidHeadersCountAsPresent(t *testing.T) {
	t.Setenv(HeadersEnv, `["not-object"]`)
	if _, ok, err := FromEnv(); err == nil || !ok {
		t.Fatalf("FromEnv invalid headers: ok=%v err=%v", ok, err)
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
