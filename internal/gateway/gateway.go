// Package gateway resolves the model gateway credential a local Telos run uses.
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

const (
	ModeManaged = "managed"
	ModeBYO     = "byo"
)

// Credential is the Responses API endpoint and key a run should use.
type Credential struct {
	BaseURL string
	APIKey  string
	Cleanup func() error
}

// Resolve chooses the local gateway credential for a session.
func Resolve(sessionID string) (Credential, error) {
	cfg := config.LoadConfig()
	if base, key := envGateway(); base != "" || key != "" {
		if base == "" || key == "" {
			return Credential{}, fmt.Errorf("both TELOS_LITELLM_BASE_URL and TELOS_LITELLM_API_KEY are required")
		}
		return Credential{BaseURL: base, APIKey: key}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Gateway.Mode))
	if mode == "" {
		if cfg.Gateway.BaseURL != "" || cfg.Gateway.APIKey != "" {
			mode = ModeBYO
		} else if cfg.AuthToken != "" {
			mode = ModeManaged
		}
	}
	switch mode {
	case ModeBYO:
		base := strings.TrimRight(strings.TrimSpace(cfg.Gateway.BaseURL), "/")
		key := strings.TrimSpace(cfg.Gateway.APIKey)
		if base == "" || key == "" {
			return Credential{}, fmt.Errorf("BYO gateway requires gateway.base_url and gateway.api_key")
		}
		return Credential{BaseURL: base, APIKey: key}, nil
	case ModeManaged:
		client, err := cloud.BillingClient()
		if err != nil {
			return Credential{}, err
		}
		key, err := client.MintSessionKey(sessionID)
		if err != nil {
			return Credential{}, err
		}
		return Credential{
			BaseURL: key.BaseURL,
			APIKey:  key.APIKey,
			Cleanup: func() error {
				return client.ReconcileSession(key.SessionID, true)
			},
		}, nil
	default:
		return Credential{}, fmt.Errorf("run `telos login` for managed gateway access or `telos configure gateway --mode byo --base-url URL --api-key KEY`")
	}
}

func envGateway() (string, string) {
	base := firstEnv("TELOS_LITELLM_BASE_URL", "TELOS_API_BASE_URL", "TELOS_BASE_URL")
	key := firstEnv("TELOS_LITELLM_API_KEY", "TELOS_API_KEY")
	return strings.TrimRight(base, "/"), key
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// ProbeResponses checks that baseURL looks like an OpenAI Responses endpoint.
func ProbeResponses(baseURL, apiKey, model string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	model = strings.TrimSpace(model)
	if baseURL == "" || apiKey == "" {
		return fmt.Errorf("base URL and API key are required")
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"input": []map[string]string{{
			"role":    "user",
			"content": "ok",
		}},
		"max_output_tokens": 1,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s/responses returned HTTP %d", baseURL, resp.StatusCode)
	}
	return nil
}
