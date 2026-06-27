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

type billingClient struct {
	endpoint string
	token    string
	envID    string
	http     *http.Client
}

func newBillingClient(cfg BillingConfig) *billingClient {
	return &billingClient{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		token:    strings.TrimSpace(cfg.Token),
		envID:    strings.TrimSpace(cfg.EnvID),
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *billingClient) configured() bool {
	return c != nil && c.endpoint != "" && c.envID != ""
}

func (c *billingClient) MintSessionKey(sessionID, parentSessionID, userAuthorization string) (controlSessionKey, error) {
	if !c.configured() {
		return controlSessionKey{}, fmt.Errorf("billing minting is not configured")
	}
	if strings.TrimSpace(parentSessionID) != "" && strings.TrimSpace(c.token) == "" && strings.TrimSpace(userAuthorization) == "" {
		return controlSessionKey{}, fmt.Errorf("billing service token is required to mint a child session key")
	}
	bodyMap := map[string]string{"env_id": c.envID}
	if parentSessionID = strings.TrimSpace(parentSessionID); parentSessionID != "" {
		bodyMap["parent_session_id"] = parentSessionID
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return controlSessionKey{}, err
	}
	req, err := http.NewRequest(http.MethodPost, c.endpoint+"/api/internal/sessions/"+sessionID+"/mint", bytes.NewReader(body))
	if err != nil {
		return controlSessionKey{}, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if strings.TrimSpace(userAuthorization) != "" {
		req.Header.Set("X-Telos-User-Authorization", strings.TrimSpace(userAuthorization))
	}
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
		return controlSessionKey{}, fmt.Errorf("billing returned invalid session key")
	}
	return controlSessionKey{
		SessionID: raw.SessionID,
		BaseURL:   strings.TrimRight(raw.BaseURL, "/"),
		APIKey:    raw.APIKey,
		KeyAlias:  raw.KeyAlias,
	}, nil
}

func (c *billingClient) ReconcileSession(sessionID string, terminal bool) error {
	if !c.configured() {
		return fmt.Errorf("billing reconciliation is not configured")
	}
	if strings.TrimSpace(c.token) == "" {
		return fmt.Errorf("billing service token is required to reconcile a cloud session")
	}
	path := c.endpoint + "/api/billing/reconcile/" + strings.TrimSpace(sessionID)
	if terminal {
		path += "?terminal=true"
	}
	req, err := http.NewRequest(http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reconcile billing session: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
