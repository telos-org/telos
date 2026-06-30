// Package cloud provides the Telos Cloud API client.
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
	DefaultAPIEndpoint = "https://api.usetelos.ai"
	DefaultTimeout     = 30 * time.Second
	UserAgent          = "telos-cli"
)

type PackageVersionRecord struct {
	Scope     string `json:"scope"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Ref       string `json:"ref"`
	Digest    string `json:"digest"`
	CreatedAt string `json:"created_at"`
}

type DeploymentRecord struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	State          string  `json:"state"`
	PackageRef     string  `json:"package_ref"`
	PackageDigest  string  `json:"package_digest"`
	RuntimeVersion *string `json:"runtime_version,omitempty"`
	ServiceURL     *string `json:"service_url,omitempty"`
	DashboardURL   *string `json:"dashboard_url,omitempty"`
	FailureReason  *string `json:"failure_reason,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

type DeploymentListResponse struct {
	Deployments []DeploymentRecord `json:"deployments"`
}

// Client is a Telos Cloud API client.
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

func (c *Client) PublishPackageVersion(scope, name, version string, data []byte) (*PackageVersionRecord, error) {
	path := "/api/packages/" + url.PathEscape(scope) + "/" + url.PathEscape(name) + "/versions/" + url.PathEscape(version)
	resp, err := c.doRaw("PUT", path, data, "application/gzip")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var record PackageVersionRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) CreateDeployment(name, packageRef string) (*DeploymentRecord, error) {
	body, err := json.Marshal(map[string]string{
		"name":        name,
		"package_ref": packageRef,
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/deployments", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var response DeploymentRecord
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) UpdateDeployment(deploymentID, packageRef string) (*DeploymentRecord, error) {
	body, err := json.Marshal(map[string]string{"package_ref": packageRef})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("PUT", "/api/deployments/"+url.PathEscape(deploymentID), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response DeploymentRecord
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListDeployments() ([]DeploymentRecord, error) {
	resp, err := c.do("GET", "/api/deployments", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response DeploymentListResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response.Deployments, nil
}

func (c *Client) GetDeployment(deploymentID string) (*DeploymentRecord, error) {
	resp, err := c.do("GET", "/api/deployments/"+url.PathEscape(deploymentID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response DeploymentRecord
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) DeleteDeployment(deploymentID string) (*DeploymentRecord, error) {
	resp, err := c.do("DELETE", "/api/deployments/"+url.PathEscape(deploymentID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response DeploymentRecord
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetDeploymentTranscript(deploymentID string) (string, error) {
	resp, err := c.do("GET", "/api/deployments/"+url.PathEscape(deploymentID)+"/transcript", nil)
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

// StreamEvents follows the cloud event stream for a session.
func (c *Client) StreamEvents(ctx context.Context, id string, onEvent func(map[string]any) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"/api/sessions/"+id+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", UserAgent)
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
	req.Header.Set("User-Agent", UserAgent)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.HTTP.Do(req)
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
