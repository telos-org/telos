// Package hosted provides the hosted Sessions API client.
package hosted

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/sessionapi"
)

const (
	DefaultAPIEndpoint = "https://api.usetelos.ai"
	DefaultTimeout     = 30 * time.Second
)

// Environment describes a hosted Telos environment from the control plane.
type Environment struct {
	ID             string
	Handle         string
	EnvAPIKey      string
	State          string
	HasRecoverable bool
}

// Client is a hosted Sessions API client.
type Client struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
}

// NewClient creates a client from config.
func NewClient(endpoint, token string) *Client {
	return &Client{
		Endpoint: NormalizeEndpoint(endpoint),
		Token:    token,
		HTTP:     &http.Client{Timeout: DefaultTimeout},
	}
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

// NewClientFromConfig creates a client from the user's config file.
func NewClientFromConfig() (*Client, error) {
	return ControlClient()
}

// NewEnvironmentClient resolves envID through the control plane and returns a
// client for that environment-local Sessions API.
func NewEnvironmentClient(envID string) (*Client, *Environment, error) {
	if envID == "" {
		return nil, nil, fmt.Errorf("--env is required for hosted session commands")
	}
	env, err := ResolveEnvironment(envID)
	if err != nil {
		return nil, nil, err
	}
	if env.Handle == "" {
		return nil, nil, fmt.Errorf("environment %s has no handle", envID)
	}
	if env.EnvAPIKey == "" {
		return nil, nil, fmt.Errorf("environment %s has no local API key; recover access first", envID)
	}
	return NewClient("https://"+env.Handle, env.EnvAPIKey), env, nil
}

// ResolveEnvironment returns the control-plane record plus local/recovered API
// key for an owned environment.
func ResolveEnvironment(envID string) (*Environment, error) {
	control, err := ControlClient()
	if err != nil {
		return nil, err
	}
	envs, err := control.ListEnvironments()
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		if env.ID != envID {
			continue
		}
		if access, ok := config.EnvironmentAccessByID(envID); ok {
			env.EnvAPIKey = access.EnvAPIKey
			return &env, nil
		}
		if !env.HasRecoverable {
			return nil, fmt.Errorf("no local access for %s; create a fresh environment", envID)
		}
		recovered, err := control.IssueEnvironmentAccess(envID)
		if err != nil {
			return nil, err
		}
		if err := config.SaveEnvironmentAccessEntry(config.EnvironmentAccess{
			ID:        recovered.ID,
			EnvAPIKey: recovered.EnvAPIKey,
		}); err != nil {
			return nil, err
		}
		return recovered, nil
	}
	return nil, fmt.Errorf("environment %s not found", envID)
}

// CreateEnvironment creates a new hosted environment through the control plane.
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
	if env.ID == "" || env.Handle == "" || env.EnvAPIKey == "" {
		return nil, fmt.Errorf("control plane returned invalid environment")
	}
	return &env, nil
}

// WaitForEnvironment waits until an environment-local API is reachable.
func WaitForEnvironment(handle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := NormalizeEndpoint(handle) + "/api/healthz"
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil && resp != nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		lastErr = err
		time.Sleep(5 * time.Second)
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

// CreateSession creates a new session via the hosted API.
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

// ListEnvironments lists hosted environments from the control plane.
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

// IssueEnvironmentAccess recovers a local environment API key.
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
	if env.ID == "" || env.Handle == "" || env.EnvAPIKey == "" {
		return nil, fmt.Errorf("control plane returned invalid environment access")
	}
	return &env, nil
}

// ListSessions lists sessions from the hosted API.
func (c *Client) ListSessions(limit int) ([]sessionapi.Session, error) {
	path := "/api/sessions"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
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
	return listResp.Sessions, nil
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

func (c *Client) do(method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.Endpoint+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.HTTP.Do(req)
}

func environmentFromJSON(raw map[string]any) Environment {
	return Environment{
		ID:             stringValue(raw["id"]),
		Handle:         stringValue(raw["env_handle"]),
		EnvAPIKey:      stringValue(raw["env_api_key"]),
		State:          stringValue(raw["state"]),
		HasRecoverable: boolValue(raw["has_recoverable_env_access"]),
	}
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
	var m map[string]string
	if json.Unmarshal(data, &m) == nil {
		if detail, ok := m["detail"]; ok {
			return fmt.Errorf("%s (HTTP %d)", detail, resp.StatusCode)
		}
	}
	if len(data) > 0 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
