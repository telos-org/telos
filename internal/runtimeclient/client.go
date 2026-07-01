// Package runtimeclient talks to an environment-local telosd Sessions API.
package runtimeclient

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

	"github.com/telos-org/telos/internal/sessionapi"
)

const (
	DefaultTimeout = 30 * time.Second
	UserAgent      = "telos-cli"
)

type Client struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
}

func New(endpoint string, token string) *Client {
	return &Client{
		Endpoint: normalizeEndpoint(endpoint),
		Token:    token,
		HTTP:     &http.Client{Timeout: DefaultTimeout},
	}
}

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

func (c *Client) StreamEvents(ctx context.Context, id string, onEvent func(map[string]any) error) error {
	return c.streamEvents(ctx, "/api/sessions/"+id+"/events", func(data []byte) error {
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode event stream payload: %w", err)
		}
		return onEvent(event)
	})
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

func (c *Client) do(method string, path string, body []byte) (*http.Response, error) {
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
	req.Header.Set("User-Agent", UserAgent)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.HTTP.Do(req)
}

func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = "https://" + endpoint
	}
	return endpoint
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
