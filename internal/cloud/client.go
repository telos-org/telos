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
	"strings"
	"time"

	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/sessionapi"
)

const (
	DefaultAPIEndpoint = "https://api.usetelos.ai"
	DefaultTimeout     = 30 * time.Second
)

// Environment describes a cloud Telos environment from the control plane.
type Environment struct {
	ID             string
	Handle         string
	AccessToken    string
	State          string
	HasRecoverable bool
}

// Client is a cloud Sessions API client.
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
	return NewClient("https://"+env.Handle, env.AccessToken), env, nil
}

// ResolveEnvironment returns the control-plane record plus local/recovered
// scoped access token for an owned environment.
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
			env.AccessToken = access.Token
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
			ID:    recovered.ID,
			Token: recovered.AccessToken,
		}); err != nil {
			return nil, err
		}
		return recovered, nil
	}
	return nil, fmt.Errorf("environment %s not found", envID)
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
