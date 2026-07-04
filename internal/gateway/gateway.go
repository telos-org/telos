// Package gateway resolves the model gateway credential a local Telos run uses.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/gatewaycred"
)

const (
	ModeManaged = "managed"
	ModeBYO     = "byo"

	TransportOpenAISync   = gatewaycred.TransportOpenAISync
	TransportBifrostAsync = gatewaycred.TransportBifrostAsync

	KindOpenAI  = gatewaycred.KindOpenAI
	KindBifrost = gatewaycred.KindBifrost
)

// Credential is a resolved gateway credential plus any policy cleanup hook.
type Credential struct {
	gatewaycred.Credential
	Cleanup func() error
}

type ProbeConfig struct {
	Transport gatewaycred.Transport
	Kind      gatewaycred.Kind
	Headers   map[string]string
}

// Enabled reports whether the caller explicitly configured Telos gateway
// routing. A login token alone must not opt existing Pi/provider setups into
// managed billing.
func Enabled() bool {
	cfg := config.LoadConfig()
	if _, ok, err := gatewaycred.FromEnv(); err != nil {
		return true
	} else if ok {
		return true
	}
	return strings.TrimSpace(cfg.Gateway.Mode) != "" ||
		strings.TrimSpace(cfg.Gateway.BaseURL) != "" ||
		strings.TrimSpace(cfg.Gateway.APIKey) != "" ||
		strings.TrimSpace(cfg.Gateway.Transport) != "" ||
		strings.TrimSpace(cfg.Gateway.Kind) != "" ||
		len(cfg.Gateway.Headers) > 0
}

// Resolve chooses the local gateway credential for a session.
func Resolve(sessionID string) (Credential, error) {
	cfg := config.LoadConfig()
	if cred, ok, err := gatewaycred.FromEnv(); err != nil {
		return Credential{}, err
	} else if ok {
		if cred.BaseURL == "" || cred.APIKey == "" {
			return Credential{}, fmt.Errorf("both TELOS_GATEWAY_BASE_URL and TELOS_GATEWAY_API_KEY are required")
		}
		if cred.Transport == gatewaycred.TransportBifrostAsync && !strings.HasSuffix(cred.BaseURL, "/openai") {
			return Credential{}, fmt.Errorf("bifrost_async via the OpenAI SDK requires TELOS_GATEWAY_BASE_URL to end in /openai")
		}
		return Credential{Credential: cred}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Gateway.Mode))
	if mode == "" {
		if cfg.Gateway.BaseURL != "" || cfg.Gateway.APIKey != "" {
			mode = ModeBYO
		}
	}
	switch mode {
	case ModeBYO:
		if strings.TrimSpace(cfg.Gateway.BaseURL) == "" || strings.TrimSpace(cfg.Gateway.APIKey) == "" {
			return Credential{}, fmt.Errorf("BYO gateway requires gateway.base_url and gateway.api_key")
		}
		cred, err := gatewaycred.NormalizeWithEnvPolicy(gatewaycred.Credential{
			BaseURL:   cfg.Gateway.BaseURL,
			APIKey:    cfg.Gateway.APIKey,
			Transport: gatewaycred.Transport(cfg.Gateway.Transport),
			Kind:      gatewaycred.Kind(cfg.Gateway.Kind),
			Headers:   cfg.Gateway.Headers,
		})
		if err != nil {
			return Credential{}, err
		}
		if cred.Transport == gatewaycred.TransportBifrostAsync && !strings.HasSuffix(cred.BaseURL, "/openai") {
			return Credential{}, fmt.Errorf("bifrost_async via the OpenAI SDK requires gateway.base_url to end in /openai")
		}
		return Credential{Credential: cred}, nil
	case ModeManaged:
		client, err := cloud.NewBillingClientFromConfig()
		if err != nil {
			return Credential{}, err
		}
		key, err := client.MintSessionKey(sessionID)
		if err != nil {
			return Credential{}, err
		}
		cred, err := gatewaycred.NormalizeWithEnvPolicy(gatewaycred.Credential{
			BaseURL:       key.BaseURL,
			APIKey:        key.APIKey,
			Transport:     gatewaycred.Transport(key.Transport),
			Kind:          gatewaycred.Kind(key.Kind),
			Headers:       key.Headers,
			CostHardLimit: true,
		})
		if err != nil {
			return Credential{}, err
		}
		return Credential{
			Credential: cred,
			Cleanup: func() error {
				return client.ReconcileSession(key.SessionID, true)
			},
		}, nil
	default:
		return Credential{}, fmt.Errorf("run `telos login` for managed gateway access or `telos configure gateway --mode byo --base-url URL --api-key KEY`")
	}
}

// ProbeResponses checks that baseURL looks like the configured Responses
// transport endpoint.
func ProbeResponses(baseURL, apiKey, model string, cfg ProbeConfig) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	model = strings.TrimSpace(model)
	if baseURL == "" || apiKey == "" {
		return fmt.Errorf("base URL and API key are required")
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	transport, _, err := gatewaycred.NormalizeTransportAndKind(string(cfg.Transport), string(cfg.Kind))
	if err != nil {
		return err
	}
	if transport == gatewaycred.TransportBifrostAsync && !strings.HasSuffix(baseURL, "/openai") {
		return fmt.Errorf("bifrost_async via the OpenAI SDK requires the /openai endpoint")
	}
	body, _ := json.Marshal(map[string]any{
		"model":             model,
		"input":             "Reply with exactly OK.",
		"max_output_tokens": 64,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := postProbe(ctx, baseURL, apiKey, body, cfg.Headers, asyncSubmitHeader(transport), &http.Client{Timeout: 20 * time.Second})
	if err != nil {
		return err
	}
	if transport == gatewaycred.TransportBifrostAsync && isProbePending(result.Status) {
		if result.ID == "" {
			return fmt.Errorf("bifrost_async probe returned no async job ID")
		}
		jobID := result.ID
		for {
			if err := sleepProbe(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			result, err = postProbe(ctx, baseURL, apiKey, body, cfg.Headers, map[string]string{"x-bf-async-id": jobID}, &http.Client{Timeout: 20 * time.Second})
			if err != nil {
				return err
			}
			if !isProbePending(result.Status) {
				break
			}
		}
	}
	return validateProbeResult(baseURL, result)
}

func postProbe(ctx context.Context, baseURL, apiKey string, body []byte, headers map[string]string, perRequest map[string]string, client *http.Client) (probeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return probeResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range perRequest {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return probeResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return probeResponse{}, fmt.Errorf("POST %s/responses returned HTTP %d", baseURL, resp.StatusCode)
	}
	var result probeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return probeResponse{}, err
	}
	return result, nil
}

type probeResponse struct {
	ID         string             `json:"id"`
	Status     string             `json:"status"`
	OutputText string             `json:"output_text"`
	Error      probeResponseError `json:"error"`
	Output     []probeOutputItem  `json:"output"`
}

type probeResponseError struct {
	Message string `json:"message"`
}

type probeOutputItem struct {
	Content []probeOutputContent `json:"content"`
}

type probeOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func validateProbeResult(baseURL string, result probeResponse) error {
	switch result.Status {
	case "completed":
		if strings.TrimSpace(result.text()) == "" {
			return fmt.Errorf("POST %s/responses completed but returned no output text", baseURL)
		}
		return nil
	case "failed":
		if msg := strings.TrimSpace(result.Error.Message); msg != "" {
			return fmt.Errorf("POST %s/responses failed: %s", baseURL, msg)
		}
		return fmt.Errorf("POST %s/responses failed", baseURL)
	case "cancelled":
		return fmt.Errorf("POST %s/responses was cancelled", baseURL)
	case "incomplete":
		return fmt.Errorf("POST %s/responses returned incomplete", baseURL)
	default:
		if result.Status == "" {
			return fmt.Errorf("POST %s/responses returned no status", baseURL)
		}
		return fmt.Errorf("POST %s/responses returned non-terminal status %q", baseURL, result.Status)
	}
}

func (r probeResponse) text() string {
	if text := strings.TrimSpace(r.OutputText); text != "" {
		return text
	}
	var out strings.Builder
	for _, item := range r.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" {
				out.WriteString(content.Text)
			}
		}
	}
	return out.String()
}

func asyncSubmitHeader(transport gatewaycred.Transport) map[string]string {
	if transport == gatewaycred.TransportBifrostAsync {
		return map[string]string{"x-bf-async": "true"}
	}
	return nil
}

func isProbePending(status string) bool {
	switch status {
	case "pending", "processing", "queued", "in_progress":
		return true
	default:
		return false
	}
}

func sleepProbe(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ValidateTransportAndKind validates and normalizes gateway transport/kind values.
func ValidateTransportAndKind(rawTransport, rawKind string) (gatewaycred.Transport, gatewaycred.Kind, error) {
	return gatewaycred.NormalizeTransportAndKind(rawTransport, rawKind)
}
