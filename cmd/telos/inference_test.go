package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestCloudApplyModelPrecedenceIgnoresLegacyDefault(t *testing.T) {
	pkg := testApplyPackage(t)
	for _, tt := range []struct {
		name  string
		env   string
		flags []string
		want  *cloud.InferenceSelection
	}{
		{name: "workspace default"},
		{
			name: "environment override",
			env:  "telos/max",
			want: &cloud.InferenceSelection{Source: "managed", Tier: "max"},
		},
		{
			name:  "flag overrides environment",
			env:   "telos/max",
			flags: []string{"--model", "telos/default"},
			want:  &cloud.InferenceSelection{Source: "managed", Tier: "default"},
		},
		{
			name:  "empty flag defers to workspace",
			env:   "telos/max",
			flags: []string{"--model", ""},
		},
		{
			name:  "subscription override",
			flags: []string{"--model", "openai-rohan/gpt-5.6-sol"},
			want: &cloud.InferenceSelection{
				Source: "subscription", ConnectionID: "conn_rohan", Model: "gpt-5.6-sol",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := make(chan map[string]json.RawMessage, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/packages/telos/demo/versions/1.2.3":
					_ = json.NewEncoder(w).Encode(map[string]string{
						"scope": "telos", "name": "demo", "version": "1.2.3",
						"ref": "@telos/demo:1.2.3", "digest": pkg.Digest,
					})
				case r.Method == http.MethodGet && r.URL.Path == "/api/packages/telos/demo/versions/1.2.3/bundle":
					_, _ = w.Write(pkg.Bytes)
				case r.Method == http.MethodGet && r.URL.Path == "/api/inference/connections":
					_, _ = w.Write([]byte(`{"connections":[{"id":"conn_rohan","name":"openai-rohan","provider":"chatgpt-codex","status":"connected"}]}`))
				case r.Method == http.MethodPost && r.URL.Path == "/api/deployments":
					var request map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode deployment request: %v", err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					requests <- request
					_, _ = w.Write([]byte(`{"id":"sess_test","name":"demo","state":"provisioning"}`))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			configureCloudTest(t, server.URL)
			for _, name := range []string{"TELOS_CONTEXT", "TELOS_THINKING", "TELOS_MAX_COST_USD", "TELOS_SESSION_ID"} {
				t.Setenv(name, "")
			}
			t.Setenv("TELOS_MODEL", tt.env)
			if err := os.WriteFile(os.Getenv(config.ConfigPathEnv), []byte("default_model: telos/max\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			captureStdout(t, func() {
				cmdApply(append([]string{"@telos/demo:1.2.3", "--json"}, tt.flags...))
			})

			var request map[string]json.RawMessage
			select {
			case request = <-requests:
			default:
				t.Fatal("apply did not create a deployment")
			}
			if _, ok := request["agent_model"]; ok {
				t.Fatal("apply sent a legacy agent_model override")
			}
			raw, present := request["inference"]
			if tt.want == nil {
				if present {
					t.Fatalf("apply should defer to the workspace, got inference %s", raw)
				}
				return
			}
			var got cloud.InferenceSelection
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got != *tt.want {
				t.Fatalf("inference = %#v, want %#v", got, tt.want)
			}
		})
	}
}
