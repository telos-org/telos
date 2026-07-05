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

type SessionRecord struct {
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

type SessionOpenResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// The hosted control API still exposes cloud sessions at /api/deployments.
// Keep that wire contract here and expose session-shaped methods to the CLI.
type sessionListResponse struct {
	Sessions []SessionRecord `json:"deployments"`
}

type deploymentLogEventsResponse struct {
	Events []deploymentLogEvent `json:"events"`
}

type deploymentLogEvent struct {
	Event       string         `json:"event"`
	Timestamp   *string        `json:"ts,omitempty"`
	Time        *string        `json:"time,omitempty"`
	SessionID   *string        `json:"session_id,omitempty"`
	SpecIndex   *int           `json:"spec_index,omitempty"`
	SpecName    *string        `json:"spec_name,omitempty"`
	SpecDirName *string        `json:"spec_dir_name,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
	Message     string         `json:"message,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

func (event deploymentLogEvent) asSessionEvent() sessionapi.SessionEvent {
	data := event.Data
	if data == nil {
		data = map[string]any{}
	}
	if event.Message != "" {
		data["message"] = event.Message
	}
	for key, value := range event.Metadata {
		data[key] = value
	}
	timestamp := event.Timestamp
	if timestamp == nil {
		timestamp = event.Time
	}
	return sessionapi.SessionEvent{
		Event:       event.Event,
		Timestamp:   timestamp,
		SessionID:   event.SessionID,
		SpecIndex:   event.SpecIndex,
		SpecName:    event.SpecName,
		SpecDirName: event.SpecDirName,
		Data:        data,
	}
}

// Client is a Telos Cloud API client.
type Client struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
}

type APIError struct {
	StatusCode int
	Detail     string
}

func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Detail, e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func IsStatus(err error, statusCode int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
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

func (c *Client) CreateSession(name, packageRef string) (*SessionRecord, error) {
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
	var response SessionRecord
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) UpdateSession(sessionID, packageRef string) (*SessionRecord, error) {
	body, err := json.Marshal(map[string]string{"package_ref": packageRef})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("PUT", "/api/deployments/"+url.PathEscape(sessionID), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response SessionRecord
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) ListSessions() ([]SessionRecord, error) {
	resp, err := c.do("GET", "/api/deployments", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response sessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response.Sessions, nil
}

func (c *Client) GetSession(sessionID string) (*SessionRecord, error) {
	resp, err := c.do("GET", "/api/deployments/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response SessionRecord
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) DeleteSession(sessionID string) (*SessionRecord, error) {
	resp, err := c.do("DELETE", "/api/deployments/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response SessionRecord
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) OpenSession(sessionID, target, path string) (*SessionOpenResponse, error) {
	body, err := json.Marshal(map[string]string{
		"target": target,
		"path":   path,
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/deployments/"+url.PathEscape(sessionID)+"/open", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response SessionOpenResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) GetSessionLogs(sessionID string) ([]sessionapi.SessionEvent, error) {
	resp, err := c.do("GET", "/api/deployments/"+url.PathEscape(sessionID)+"/logs", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response deploymentLogEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	events := make([]sessionapi.SessionEvent, 0, len(response.Events))
	for _, event := range response.Events {
		events = append(events, event.asSessionEvent())
	}
	return events, nil
}

func (c *Client) StreamSessionLogs(ctx context.Context, sessionID string, onEvent func(sessionapi.SessionEvent) error) error {
	return c.streamEvents(ctx, "/api/deployments/"+url.PathEscape(sessionID)+"/logs", func(data []byte) error {
		var event deploymentLogEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode session log event: %w", err)
		}
		return onEvent(event.asSessionEvent())
	})
}

// NormalizeEndpoint cleans up an API endpoint URL.
func NormalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = "https://" + endpoint
	}
	return endpoint
}

func (c *Client) streamEvents(ctx context.Context, path string, onData func([]byte) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+path, nil)
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
		if err := onData([]byte(payload)); err != nil {
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
			return &APIError{StatusCode: resp.StatusCode, Detail: detail}
		}
		if detail, ok := m["detail"]; ok {
			return &APIError{StatusCode: resp.StatusCode, Detail: fmt.Sprint(detail)}
		}
	}
	if len(data) > 0 {
		return &APIError{StatusCode: resp.StatusCode, Detail: string(data)}
	}
	return &APIError{StatusCode: resp.StatusCode}
}
