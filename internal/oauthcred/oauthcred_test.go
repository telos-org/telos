package oauthcred

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPKCEChallenge(t *testing.T) {
	verifier, challenge, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if verifier == "" || challenge == "" || strings.Contains(challenge, "=") {
		t.Fatalf("bad pkce verifier/challenge: %q %q", verifier, challenge)
	}
}

func TestSaveUses0600Perms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	if err := Save(path, Token{AccessToken: "access"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%#o, want 0600", got)
	}
}

func TestRefreshUsesRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
			t.Fatalf("bad form: %v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"expires_in":   60,
		})
	}))
	defer server.Close()
	old := TokenURL
	TokenURL = server.URL
	defer func() { TokenURL = old }()
	got, err := Refresh(context.Background(), server.Client(), Token{RefreshToken: "old-refresh", AccountID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "old-refresh" || got.AccountID != "acct" {
		t.Fatalf("token=%#v", got)
	}
}
