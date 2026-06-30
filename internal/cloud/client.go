// Package cloud provides the cloud Sessions API client.
package cloud

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
)

const (
	DefaultAPIEndpoint               = "https://api.usetelos.ai"
	DefaultBillingEndpoint           = "https://billing.usetelos.ai"
	DefaultTimeout                   = 30 * time.Second
	ForwardedUserAuthorizationHeader = "X-Telos-User-Authorization"
)

// Environment describes a cloud Telos environment from the control plane.
type Environment struct {
	ID             string
	Handle         string
	AccessToken    string
	State          string
	HasRecoverable bool
}

// SessionKey is a budget-capped model gateway key minted by billing.
type SessionKey struct {
	SessionID string
	BaseURL   string
	APIKey    string
	Transport string
	Kind      string
	Headers   map[string]string
	BudgetUSD float64
	KeyAlias  string
}

// Balance is the caller's current managed compute-unit balance.
type Balance struct {
	ComputeUnits float64
}

type ApplyPackageRecord struct {
	Digest    string `json:"digest"`
	SizeBytes int    `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
}

type CatalogSpecRecord struct {
	Name          string `json:"name"`
	PackageDigest string `json:"package_digest"`
	Visibility    string `json:"visibility"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type CatalogSpecPushResponse struct {
	Operation string            `json:"operation"`
	Spec      CatalogSpecRecord `json:"spec"`
}

type EnvironmentSessionRecord struct {
	EnvID         string `json:"env_id"`
	Name          string `json:"name"`
	PackageDigest string `json:"package_digest"`
	DesiredState  string `json:"desired_state"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type EnvironmentSessionApplyResponse struct {
	Operation string                   `json:"operation"`
	Session   EnvironmentSessionRecord `json:"session"`
}

// Client is a cloud Sessions API client.
type Client struct {
	Endpoint           string
	Token              string
	ForwardedUserToken string
	HTTP               *http.Client
}

// NewClient creates a client from config.
func NewClient(endpoint, token string) *Client {
	return &Client{
		Endpoint: NormalizeEndpoint(endpoint),
		Token:    token,
		HTTP:     &http.Client{Timeout: DefaultTimeout},
	}
}

// NewEnvironmentAPIClient creates a client for an environment-local Sessions API.
func NewEnvironmentAPIClient(endpoint, token string) *Client {
	cfg := config.LoadConfig()
	client := NewClient(endpoint, token)
	client.ForwardedUserToken = cfg.AuthToken
	return client
}

// ControlClient returns a client for the configured Telos control plane.
func ControlClient() (*Client, error) {
	cfg := config.LoadConfig()
	endpoint := cfg.APIEndpoint
	if endpoint == "" {
		endpoint = DefaultAPIEndpoint
	}
	token := cfg.AuthToken
	if token == "" {
		return nil, fmt.Errorf("not logged in; run `telos login` first")
	}
	return NewClient(endpoint, token), nil
}

// BillingClient returns a client for the configured Telos billing service.
func BillingClient() (*Client, error) {
	cfg := config.LoadConfig()
	endpoint := cfg.BillingEndpoint
	if endpoint == "" {
		endpoint = DefaultBillingEndpoint
	}
	token := cfg.AuthToken
	if token == "" {
		return nil, fmt.Errorf("not logged in; run `telos login` first")
	}
	return NewClient(endpoint, token), nil
}

// NewClientFromConfig creates a client from the user's config file.
func NewClientFromConfig() (*Client, error) {
	return ControlClient()
}

// NewEnvironmentClient resolves envID through the control plane and returns a
// client for that environment-local Sessions API.
func NewEnvironmentClient(envID string) (*Client, *Environment, error) {
	if envID == "" {
		return nil, nil, fmt.Errorf("--env is required for cloud session commands")
	}
	env, err := ResolveEnvironment(envID)
	if err != nil {
		return nil, nil, err
	}
	if env.Handle == "" {
		return nil, nil, fmt.Errorf("environment %s has no handle", envID)
	}
	if env.AccessToken == "" {
		return nil, nil, fmt.Errorf("environment %s has no local access token; recover access first", envID)
	}
	return NewEnvironmentAPIClient("https://"+env.Handle, env.AccessToken), env, nil
}

// ResolveEnvironment returns the control-plane record plus local/recovered
// scoped access token for an owned environment.
func ResolveEnvironment(envID string) (*Environment, error) {
	control, err := ControlClient()
	if err != nil {
		return nil, err
	}
	env, err := control.GetEnvironment(envID)
	if err != nil {
		return nil, err
	}
	if access, ok := config.EnvironmentAccessByID(envID); ok {
		env.AccessToken = access.Token
		return env, nil
	}
	if !env.HasRecoverable {
		return nil, fmt.Errorf("no local access for %s; create a fresh environment", envID)
	}
	recovered, err := control.IssueEnvironmentAccess(envID)
	if err != nil {
		return nil, err
	}
	if err := config.SaveEnvironmentAccessEntry(config.EnvironmentAccess{
		ID:    recovered.ID,
		Token: recovered.AccessToken,
	}); err != nil {
		return nil, err
	}
	return recovered, nil
}

// CreateEnvironment creates a new cloud environment through the control plane.
func (c *Client) CreateEnvironment() (*Environment, error) {
	resp, err := c.do("POST", "/api/environments", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	env := environmentFromJSON(raw)
	if env.ID == "" || env.Handle == "" || env.AccessToken == "" {
		return nil, fmt.Errorf("control plane returned invalid environment")
	}
	return &env, nil
}

// MintSessionKey asks billing to mint a managed per-session gateway key.
func (c *Client) MintSessionKey(sessionID string) (*SessionKey, error) {
	body, err := json.Marshal(map[string]string{"session_id": sessionID})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/billing/session-key", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var raw struct {
		SessionID string            `json:"session_id"`
		BaseURL   string            `json:"base_url"`
		APIKey    string            `json:"api_key"`
		Transport string            `json:"transport"`
		Kind      string            `json:"kind"`
		Headers   map[string]string `json:"headers"`
		BudgetUSD float64           `json:"budget_usd"`
		KeyAlias  string            `json:"key_alias"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if raw.BaseURL == "" || raw.APIKey == "" {
		return nil, fmt.Errorf("billing returned invalid session key")
	}
	raw.SessionID = strings.TrimSpace(raw.SessionID)
	if raw.SessionID == "" {
		return nil, fmt.Errorf("billing returned invalid session key: missing session_id")
	}
	if raw.SessionID != strings.TrimSpace(sessionID) {
		return nil, fmt.Errorf("billing returned session key for %q, want %q", raw.SessionID, strings.TrimSpace(sessionID))
	}
	return &SessionKey{
		SessionID: raw.SessionID,
		BaseURL:   raw.BaseURL,
		APIKey:    raw.APIKey,
		Transport: raw.Transport,
		Kind:      raw.Kind,
		Headers:   cloneStringMap(raw.Headers),
		BudgetUSD: raw.BudgetUSD,
		KeyAlias:  raw.KeyAlias,
	}, nil
}

// ReconcileSession asks the control plane to settle a managed local session.
func (c *Client) ReconcileSession(sessionID string, terminal bool) error {
	path := "/api/billing/session-key/" + sessionID + "/reconcile"
	if terminal {
		path += "?terminal=true"
	}
	resp, err := c.do("POST", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	return nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Balance returns the caller's managed compute-unit balance.
func (c *Client) Balance() (*Balance, error) {
	resp, err := c.do("GET", "/api/billing/balance", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var raw struct {
		ComputeUnits float64 `json:"compute_units"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return &Balance{ComputeUnits: raw.ComputeUnits}, nil
}

func (c *Client) UploadApplyPackage(digest string, data []byte) (*ApplyPackageRecord, error) {
	resp, err := c.doRaw("PUT", "/api/catalog/packages/"+url.PathEscape(digest), data, "application/gzip")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var record ApplyPackageRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) PushCatalogSpec(name string, packageDigest string) (*CatalogSpecPushResponse, error) {
	body, err := json.Marshal(map[string]string{"package_digest": packageDigest})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("PUT", "/api/catalog/specs/"+url.PathEscape(name), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response CatalogSpecPushResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ApplyEnvironmentSession(envID, name, packageDigest string) (*EnvironmentSessionApplyResponse, error) {
	body, err := json.Marshal(map[string]string{"package_digest": packageDigest})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("PUT", "/api/environments/"+url.PathEscape(envID)+"/sessions/"+url.PathEscape(name), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response EnvironmentSessionApplyResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

// WaitForEnvironment waits until an environment-local API is reachable.
func WaitForEnvironment(handle string, timeout time.Duration) error {
	client := &http.Client{Timeout: 5 * time.Second}
	return waitForEnvironment(handle, timeout, client, 5*time.Second)
}

func waitForEnvironment(handle string, timeout time.Duration, client *http.Client, pollInterval time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := NormalizeEndpoint(handle) + "/api/healthz"
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil && resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(pollInterval)
	}
	if lastErr != nil {
		return fmt.Errorf("%s did not become ready: %w", handle, lastErr)
	}
	return fmt.Errorf("%s did not become ready", handle)
}

// NormalizeEndpoint cleans up an API endpoint URL.
func NormalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = "https://" + endpoint
	}
	return endpoint
}

// CreateSession creates a new session via the cloud API.
func (c *Client) CreateSession(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/sessions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var session sessionapi.Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}
	return &session, nil
}

// ApplySessionSpec creates or updates the root session named by the spec.
func (c *Client) ApplySessionSpec(name string, req sessionapi.SessionSpecUpdateRequest) (*sessionapi.SessionSpecUpdateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.do("PUT", "/api/sessions/"+name+"/spec", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response sessionapi.SessionSpecUpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

// ListEnvironments lists cloud environments from the control plane.
func (c *Client) ListEnvironments() ([]Environment, error) {
	resp, err := c.do("GET", "/api/environments", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var payload struct {
		Environments []map[string]any `json:"environments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	envs := make([]Environment, 0, len(payload.Environments))
	for _, raw := range payload.Environments {
		envs = append(envs, environmentFromJSON(raw))
	}
	return envs, nil
}

// GetEnvironment resolves an environment control-plane record without requiring
// or issuing an environment-local access token.
func (c *Client) GetEnvironment(envID string) (*Environment, error) {
	if envID == "" {
		return nil, fmt.Errorf("environment id is required")
	}
	envs, err := c.ListEnvironments()
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		if env.ID == envID {
			return &env, nil
		}
	}
	return nil, fmt.Errorf("environment %s not found", envID)
}

// IssueEnvironmentAccess issues a scoped environment access token.
func (c *Client) IssueEnvironmentAccess(envID string) (*Environment, error) {
	resp, err := c.do("POST", "/api/environments/"+envID+"/access", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	env := environmentFromJSON(raw)
	if env.ID == "" || env.Handle == "" || env.AccessToken == "" {
		return nil, fmt.Errorf("control plane returned invalid environment access")
	}
	return &env, nil
}

// ListSessions lists sessions from the cloud API.
func (c *Client) ListSessions(limit int, includeChildren bool) ([]sessionapi.Session, error) {
	path := "/api/sessions"
	var query []string
	if limit > 0 {
		query = append(query, fmt.Sprintf("limit=%d", limit))
	}
	if includeChildren {
		query = append(query, "include_children=true")
	}
	if len(query) > 0 {
		path += "?" + strings.Join(query, "&")
	}
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var listResp sessionapi.SessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, err
	}
	return sessionapi.SessionsFromListItems(listResp.Sessions), nil
}

// GetSession gets a session by ID.
func (c *Client) GetSession(id string) (*sessionapi.Session, error) {
	resp, err := c.do("GET", "/api/sessions/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var session sessionapi.Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}
	return &session, nil
}

// StopSession stops a session by ID.
func (c *Client) StopSession(id string) (*sessionapi.Session, error) {
	resp, err := c.do("POST", "/api/sessions/"+id+"/stop", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var session sessionapi.Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}
	return &session, nil
}

// GetTranscript gets the transcript for a session.
func (c *Client) GetTranscript(id string) (string, error) {
	resp, err := c.do("GET", "/api/sessions/"+id+"/transcript", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", readError(resp)
	}
	data, _ := io.ReadAll(resp.Body)
	return string(data), nil
}

// GetEvents gets events for a session.
func (c *Client) GetEvents(id string) ([]sessionapi.SessionEvent, error) {
	resp, err := c.do("GET", "/api/sessions/"+id+"/events", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var evResp sessionapi.SessionEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&evResp); err != nil {
		return nil, err
	}
	return evResp.Events, nil
}

// GetDiagnostics gets the production inspection payload for a session.
func (c *Client) GetDiagnostics(id string) (*sessionapi.SessionDiagnosticsResponse, error) {
	resp, err := c.do("GET", "/api/sessions/"+id+"/diagnostics", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var diagnostics sessionapi.SessionDiagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&diagnostics); err != nil {
		return nil, err
	}
	return &diagnostics, nil
}

// StreamEvents follows the cloud event stream for a session.
func (c *Client) StreamEvents(ctx context.Context, id string, onEvent func(map[string]any) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"/api/sessions/"+id+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.ForwardedUserToken != "" {
		req.Header.Set(ForwardedUserAuthorizationHeader, "Bearer "+c.ForwardedUserToken)
	}
	client := http.DefaultClient
	if c.HTTP != nil {
		clone := *c.HTTP
		clone.Timeout = 0
		client = &clone
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && len(line) == 0 {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readErr
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			if readErr != nil {
				break
			}
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event map[string]any
		if decodeErr := json.Unmarshal([]byte(payload), &event); decodeErr != nil {
			return fmt.Errorf("decode event stream payload: %w", decodeErr)
		}
		if err := onEvent(event); err != nil {
			return err
		}
		if readErr != nil {
			break
		}
	}
	return nil
}

func (c *Client) do(method, path string, body []byte) (*http.Response, error) {
	return c.doRaw(method, path, body, "application/json")
}

func (c *Client) doRaw(method, path string, body []byte, contentType string) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.Endpoint+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.ForwardedUserToken != "" {
		req.Header.Set(ForwardedUserAuthorizationHeader, "Bearer "+c.ForwardedUserToken)
	}
	return c.HTTP.Do(req)
}

func environmentFromJSON(raw map[string]any) Environment {
	return Environment{
		ID:             stringValue(raw["id"]),
		Handle:         stringValue(raw["env_handle"]),
		AccessToken:    accessTokenFromJSON(raw),
		State:          stringValue(raw["state"]),
		HasRecoverable: boolValue(raw["has_recoverable_env_access"]),
	}
}

func accessTokenFromJSON(raw map[string]any) string {
	if token := stringValue(raw["access_token"]); token != "" {
		return token
	}
	return stringValue(raw["env_api_key"])
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func boolValue(value any) bool {
	b, _ := value.(bool)
	return b
}

func readError(resp *http.Response) error {
	data, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		if detail, ok := m["detail"].(string); ok {
			return fmt.Errorf("%s (HTTP %d)", detail, resp.StatusCode)
		}
		if detail, ok := m["detail"]; ok {
			return fmt.Errorf("%v (HTTP %d)", detail, resp.StatusCode)
		}
	}
	if len(data) > 0 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
