package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func TestResolveCloudInference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/inference/connections" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"connections":[
			{"id":"conn_rohan","name":"openai-rohan","provider":"chatgpt-codex","status":"connected","account_label":"rohan@example.com","plan":"pro"},
			{"id":"conn_james","name":"openai-james","provider":"chatgpt-codex","status":"needs_attention","account_label":"james@example.com"}
		]}`))
	}))
	defer server.Close()
	client := cloud.NewClient(server.URL, "token")

	managed, err := resolveCloudInference(client, "telos/max")
	if err != nil || managed.Source != "managed" || managed.Tier != "max" {
		t.Fatalf("managed = %#v, err = %v", managed, err)
	}
	subscription, err := resolveCloudInference(client, "openai-rohan/gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if subscription.Source != "subscription" ||
		subscription.ConnectionID != "conn_rohan" ||
		subscription.Model != "gpt-5.6-sol" {
		t.Fatalf("subscription = %#v", subscription)
	}
	if _, err := resolveCloudInference(client, "openai-james/gpt-5.6-sol"); err == nil {
		t.Fatal("needs-attention connection resolved")
	}
	if _, err := resolveCloudInference(client, "missing/gpt-5.6-sol"); err == nil {
		t.Fatal("missing connection resolved")
	}
}

func TestConfiguredCloudModelUsesStoredDefaultOnlyWhenUnset(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(config.ConfigPathEnv, configPath)
	if err := config.SaveConfig(&config.Config{DefaultModel: "openai-rohan/gpt-5.6-sol"}); err != nil {
		t.Fatal(err)
	}

	got, err := configuredCloudModel("")
	if err != nil || got != "openai-rohan/gpt-5.6-sol" {
		t.Fatalf("configured model = %q, err = %v", got, err)
	}
	got, err = configuredCloudModel("telos/max")
	if err != nil || got != "telos/max" {
		t.Fatalf("explicit model = %q, err = %v", got, err)
	}
}
