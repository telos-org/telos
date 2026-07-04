package oauthcred

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

var (
	AuthURL  = "https://auth.openai.com/oauth/authorize"
	TokenURL = "https://auth.openai.com/oauth/token"
)

var CodexScopes = []string{"openid", "profile", "email", "offline_access", "api.connectors.read", "api.connectors.invoke"}

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
}

func StorePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "codex-oauth.json")
}

func Load(path string) (Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Token{}, err
	}
	var token Token
	if err := json.Unmarshal(data, &token); err != nil {
		return Token{}, err
	}
	if token.AccountID == "" {
		token.AccountID = accountIDFromIDToken(token.IDToken)
	}
	return token, nil
}

func Save(path string, token Token) error {
	if token.AccountID == "" {
		token.AccountID = accountIDFromIDToken(token.IDToken)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func NewPKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func Refresh(ctx context.Context, client *http.Client, token Token) (Token, error) {
	if strings.TrimSpace(token.RefreshToken) == "" {
		return Token{}, errors.New("codex OAuth refresh token is missing")
	}
	if client == nil {
		client = http.DefaultClient
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {token.RefreshToken},
		"client_id":     {CodexClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return Token{}, fmt.Errorf("codex OAuth refresh returned HTTP %d", resp.StatusCode)
	}
	next, err := decodeToken(resp.Body)
	if err != nil {
		return Token{}, err
	}
	if next.RefreshToken == "" {
		next.RefreshToken = token.RefreshToken
	}
	if next.AccountID == "" {
		next.AccountID = token.AccountID
	}
	return next, nil
}

func RunCodexLoopback(ctx context.Context, client *http.Client, storePath string, openBrowser bool) (Token, error) {
	if client == nil {
		client = http.DefaultClient
	}
	verifier, challenge, err := NewPKCE()
	if err != nil {
		return Token{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Token{}, err
	}
	defer listener.Close()
	redirectURI := "http://" + listener.Addr().String() + "/callback"
	state := verifier[:16]
	auth, _ := url.Parse(AuthURL)
	q := auth.Query()
	q.Set("client_id", CodexClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(CodexScopes, " "))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	auth.RawQuery = q.Encode()
	if openBrowser {
		_ = openURL(auth.String())
	}
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			errCh <- errors.New("codex OAuth state mismatch")
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- errors.New("codex OAuth authorization code missing")
			http.Error(w, "authorization code missing", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "Codex login complete. You can close this tab.")
		codeCh <- code
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	fmt.Println(auth.String())
	var code string
	select {
	case <-ctx.Done():
		_ = server.Close()
		return Token{}, ctx.Err()
	case err := <-errCh:
		_ = server.Close()
		return Token{}, err
	case code = <-codeCh:
		_ = server.Close()
	}
	token, err := exchangeCode(ctx, client, code, verifier, redirectURI)
	if err != nil {
		return Token{}, err
	}
	if err := Save(storePath, token); err != nil {
		return Token{}, err
	}
	return token, nil
}

func exchangeCode(ctx context.Context, client *http.Client, code, verifier, redirectURI string) (Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {CodexClientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return Token{}, fmt.Errorf("codex OAuth token exchange returned HTTP %d", resp.StatusCode)
	}
	return decodeToken(resp.Body)
}

func decodeToken(r io.Reader) (Token, error) {
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		IDToken      string `json:"id_token"`
		AccountID    string `json:"chatgpt_account_id"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Token{}, err
	}
	token := Token{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		TokenType:    raw.TokenType,
		IDToken:      raw.IDToken,
		AccountID:    firstNonEmpty(raw.AccountID, accountIDFromIDToken(raw.IDToken)),
	}
	if raw.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	}
	return token, nil
}

func accountIDFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	for _, key := range []string{"chatgpt_account_id", "https://api.openai.com/auth/account_id", "account_id"} {
		if value, ok := claims[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func openURL(raw string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", raw)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	return cmd.Start()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
