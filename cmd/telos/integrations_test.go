package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestIntegrationListUsesSelectedWorkspaceAndOmitsSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/account/bootstrap":
			_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[{"id":"org_team","handle":"team"}]}`))
		case "/api/secrets":
			if r.Header.Get("X-Telos-Org-Id") != "org_team" {
				t.Error("wrong workspace selected")
			}
			_, _ = w.Write([]byte(`{"secrets":[{"id":"sec_stripe","name":"Stripe","credentials":[{"key":"STRIPE_KEY","value":"private-value","config":{"token":"private-config"},"environment":{"private":"private-env"}}]},{"id":"sec_managed","name":"Managed inference","managed_by":"telos","credentials":[]}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("TELOS_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("TELOS_AUTH_TOKEN", "test-token")
	t.Setenv("TELOS_API_ENDPOINT", server.URL)
	t.Setenv("TELOS_CONTEXT", "personal")
	for _, tc := range []struct {
		name string
		run  func([]string)
	}{
		{"credentials", cmdCredentials},
		{"integrations", cmdIntegrations},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureStdout(t, func() { tc.run([]string{"list", "--context", "@team", "--json"}) })
			var result map[string]json.RawMessage
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			var credentials []integrationMetadata
			if err := json.Unmarshal(result[tc.name], &credentials); err != nil {
				t.Fatal(err)
			}
			if string(result["context"]) != `"@team"` || len(credentials) != 1 || credentials[0].ID != "sec_stripe" || credentials[0].Keys[0] != "STRIPE_KEY" {
				t.Fatalf("wrong metadata: %s", output)
			}
			if strings.Contains(output, "private-") || strings.Contains(output, "sec_managed") {
				t.Fatalf("sensitive or managed metadata exposed: %s", output)
			}
		})
	}
}

type integrationTransport func(*http.Request) (*http.Response, error)

func (f integrationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestIntegrationAddProducesWorkspaceLinkUsingReadOnlyLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/account/bootstrap" {
			t.Errorf("add must only resolve context: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[{"id":"org_personal"}]}`))
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	control := cloud.NewClient(cloud.DefaultAPIEndpoint, "test-token")
	control.HTTP.Transport = integrationTransport(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	link, err := integrationAddURL(control)
	if err != nil || link != "https://usetelos.ai/integrations/new?org=org_personal" {
		t.Fatalf("wrong personal workspace link: %s %v", link, err)
	}
	control.OrgID = "org_team"
	link, err = integrationAddURL(control)
	if err != nil || link != "https://usetelos.ai/integrations/new?org=org_team" {
		t.Fatalf("wrong team workspace link: %s %v", link, err)
	}
	if _, err := integrationAddURL(cloud.NewClient("https://other.example.com", "token")); err == nil {
		t.Fatal("custom Cloud linked to the production credential form")
	}
}
