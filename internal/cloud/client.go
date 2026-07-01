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

func (c *Client) GetDeploymentLogs(deploymentID string) ([]sessionapi.SessionEvent, error) {
	resp, err := c.do("GET", "/api/deployments/"+url.PathEscape(deploymentID)+"/logs", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response sessionapi.SessionEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response.Events, nil
}

func (c *Client) StreamDeploymentLogs(ctx context.Context, deploymentID string, onEvent func(sessionapi.SessionEvent) error) error {
	return c.streamEvents(ctx, "/api/deployments/"+url.PathEscape(deploymentID)+"/logs", func(data []byte) error {
		var event sessionapi.SessionEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode deployment log event: %w", err)
		}
		return onEvent(event)
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
