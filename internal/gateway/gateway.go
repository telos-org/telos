// Package gateway resolves the model gateway credential a local Telos run uses.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
)

const (
	ModeManaged = "managed"
	ModeBYO     = "byo"

	TransportOpenAISync   = "openai_sync"
	TransportBifrostAsync = "bifrost_async"

	KindOpenAI  = "openai"
	KindBifrost = "bifrost"
)

// Credential is the Responses API endpoint and key a run should use.
type Credential struct {
	BaseURL       string
	APIKey        string
	Transport     string
	Kind          string
	Headers       map[string]string
	ModelProfile  sessionapi.ModelProfile
	CostHardLimit bool
	Cleanup       func() error
}

type ProbeConfig struct {
	Transport string
	Kind      string
	Headers   map[string]string
}

// Enabled reports whether the caller explicitly configured Telos gateway
// routing. A login token alone must not opt existing Pi/provider setups into
// managed billing.
func Enabled() bool {
	cfg := config.LoadConfig()
	if base, key, _, _, _, err := envGateway(); err != nil {
		return true
	} else if key != "" {
		return true
	} else if base != "" {
		// A stale base URL without a gateway key must not opt direct-provider
		// setups into gateway routing. Re-read the file without env overrides so
		// an explicit saved gateway config still works.
		cfg = config.LoadConfigFile()
	}
	return strings.TrimSpace(cfg.Gateway.Mode) != "" ||
		strings.TrimSpace(cfg.Gateway.BaseURL) != "" ||
		strings.TrimSpace(cfg.Gateway.APIKey) != "" ||
		strings.TrimSpace(cfg.Gateway.Transport) != "" ||
		strings.TrimSpace(cfg.Gateway.Kind) != "" ||
		len(cfg.Gateway.Headers) > 0
}

func ManagedEnabled() bool {
	cfg := config.LoadConfig()
	if mode := strings.ToLower(strings.TrimSpace(os.Getenv(config.GatewayModeEnv))); mode != "" {
		return mode == ModeManaged
	}
	return strings.ToLower(strings.TrimSpace(cfg.Gateway.Mode)) == ModeManaged
}

// Resolve chooses the local gateway credential for a session.
func Resolve(sessionID string, modelProfile sessionapi.ModelProfile) (Credential, error) {
	modelProfile, err := sessionapi.NormalizeModelProfile(string(modelProfile))
	if err != nil {
		return Credential{}, err
	}
	cfg := config.LoadConfig()
	if base, key, transport, kind, headers, err := envGateway(); err != nil {
		return Credential{}, err
	} else if key != "" {
		if base == "" {
			return Credential{}, fmt.Errorf("both TELOS_GATEWAY_BASE_URL and TELOS_GATEWAY_API_KEY are required")
		}
		transport, kind, err = resolveTransportAndKind(transport, kind)
		if err != nil {
			return Credential{}, err
		}
		return Credential{BaseURL: base, APIKey: key, Transport: transport, Kind: kind, Headers: headers, ModelProfile: modelProfile, CostHardLimit: costHardLimitFromEnv()}, nil
	} else if base != "" {
		cfg = config.LoadConfigFile()
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Gateway.Mode))
	if mode == "" {
		if cfg.Gateway.BaseURL != "" || cfg.Gateway.APIKey != "" {
			mode = ModeBYO
		}
	}
	switch mode {
	case ModeBYO:
		base := strings.TrimRight(strings.TrimSpace(cfg.Gateway.BaseURL), "/")
		key := strings.TrimSpace(cfg.Gateway.APIKey)
		if base == "" || key == "" {
			return Credential{}, fmt.Errorf("BYO gateway requires gateway.base_url and gateway.api_key")
		}
		transport, kind, err := resolveTransportAndKind(cfg.Gateway.Transport, cfg.Gateway.Kind)
		if err != nil {
			return Credential{}, err
		}
		return Credential{BaseURL: base, APIKey: key, Transport: transport, Kind: kind, Headers: cloneHeaders(cfg.Gateway.Headers), ModelProfile: modelProfile}, nil
	case ModeManaged:
		client, err := cloud.BillingClient()
		if err != nil {
			return Credential{}, err
		}
		key, err := client.MintSessionKey(sessionID, modelProfile)
		if err != nil {
			return Credential{}, err
		}
		transport, kind, err := resolveTransportAndKind(key.Transport, key.Kind)
		if err != nil {
			return Credential{}, err
		}
		return Credential{
			BaseURL:       key.BaseURL,
			APIKey:        key.APIKey,
			Transport:     transport,
			Kind:          kind,
			Headers:       cloneHeaders(key.Headers),
			ModelProfile:  key.ModelProfile,
			CostHardLimit: true,
			Cleanup: func() error {
				return client.ReconcileSession(key.SessionID, true)
			},
		}, nil
	default:
		return Credential{}, fmt.Errorf("run `telos login` for managed gateway access or `telos configure gateway --mode byo --base-url URL --api-key KEY`")
	}
}

func costHardLimitFromEnv() bool {
	raw := strings.TrimSpace(os.Getenv("TELOS_COST_HARD_LIMIT"))
	if raw != "" {
		return strings.EqualFold(raw, "1") || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "yes")
	}
	return strings.TrimSpace(os.Getenv("TELOS_ENV_ID")) != "" &&
		(strings.TrimSpace(os.Getenv("TELOS_BILLING_ENV_TOKEN")) != "" ||
			strings.TrimSpace(os.Getenv("TELOS_BILLING_ENV_TOKEN_FILE")) != "")
}

func envGateway() (string, string, string, string, map[string]string, error) {
	headers, err := headersFromEnv()
	if err != nil {
		return "", "", "", "", nil, err
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv("TELOS_GATEWAY_BASE_URL")), "/"),
		strings.TrimSpace(os.Getenv("TELOS_GATEWAY_API_KEY")),
		strings.TrimSpace(os.Getenv("TELOS_GATEWAY_TRANSPORT")),
		strings.TrimSpace(os.Getenv("TELOS_GATEWAY_KIND")),
		headers,
		nil
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
	transport, _, err := resolveTransportAndKind(cfg.Transport, cfg.Kind)
	if err != nil {
		return err
	}
	if transport == TransportBifrostAsync && !strings.HasSuffix(baseURL, "/openai") {
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
	if transport == TransportBifrostAsync && isProbePending(result.Status) {
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

func asyncSubmitHeader(transport string) map[string]string {
	if transport == TransportBifrostAsync {
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

func resolveTransportAndKind(rawTransport, rawKind string) (string, string, error) {
	kind := strings.ToLower(strings.TrimSpace(rawKind))
	switch kind {
	case "":
	case KindOpenAI, KindBifrost:
	default:
		return "", "", fmt.Errorf("unknown TELOS_GATEWAY_KIND %q (accepted: openai, bifrost)", rawKind)
	}
	transport := strings.ToLower(strings.TrimSpace(rawTransport))
	switch transport {
	case "":
		transport = TransportOpenAISync
	case TransportOpenAISync, TransportBifrostAsync:
	default:
		return "", "", fmt.Errorf("unknown TELOS_GATEWAY_TRANSPORT %q (accepted: openai_sync, bifrost_async)", rawTransport)
	}
	if kind == "" {
		if transport == TransportBifrostAsync {
			kind = KindBifrost
		} else {
			kind = KindOpenAI
		}
	}
	return transport, kind, nil
}

// ValidateTransportAndKind validates and normalizes gateway transport/kind values.
func ValidateTransportAndKind(rawTransport, rawKind string) (string, string, error) {
	return resolveTransportAndKind(rawTransport, rawKind)
}

func headersFromEnv() (map[string]string, error) {
	raw := strings.TrimSpace(os.Getenv("TELOS_GATEWAY_HEADERS"))
	if raw == "" {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("TELOS_GATEWAY_HEADERS must be a JSON object of string values: %w", err)
	}
	return cloneHeaders(headers), nil
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
