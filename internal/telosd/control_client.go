package telosd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type controlSessionKey struct {
	SessionID string
	BaseURL   string
	APIKey    string
	KeyAlias  string
}

type controlClient struct {
	endpoint string
	token    string
	envID    string
	http     *http.Client
}

func newControlClient(cfg ControlConfig) *controlClient {
	return &controlClient{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		token:    strings.TrimSpace(cfg.Token),
		envID:    strings.TrimSpace(cfg.EnvID),
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *controlClient) configured() bool {
	return c != nil && c.endpoint != "" && c.token != "" && c.envID != ""
}

func (c *controlClient) MintSessionKey(sessionID string) (controlSessionKey, error) {
	if !c.configured() {
		return controlSessionKey{}, fmt.Errorf("control plane minting is not configured")
	}
	body, err := json.Marshal(map[string]string{"env_id": c.envID})
	if err != nil {
		return controlSessionKey{}, err
	}
	req, err := http.NewRequest(http.MethodPost, c.endpoint+"/api/internal/sessions/"+sessionID+"/mint", bytes.NewReader(body))
	if err != nil {
		return controlSessionKey{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return controlSessionKey{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return controlSessionKey{}, fmt.Errorf("mint session key: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var raw struct {
		SessionID string `json:"session_id"`
		BaseURL   string `json:"base_url"`
		APIKey    string `json:"api_key"`
		KeyAlias  string `json:"key_alias"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return controlSessionKey{}, err
	}
	if raw.BaseURL == "" || raw.APIKey == "" {
		return controlSessionKey{}, fmt.Errorf("control plane returned invalid session key")
	}
	return controlSessionKey{
		SessionID: raw.SessionID,
		BaseURL:   strings.TrimRight(raw.BaseURL, "/"),
		APIKey:    raw.APIKey,
		KeyAlias:  raw.KeyAlias,
	}, nil
}
