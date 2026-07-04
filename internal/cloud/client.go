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
	"net/url"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/gatewaycred"
	"github.com/telos-org/telos/internal/sessionapi"
)

const (
	DefaultAPIEndpoint               = "https://api.usetelos.ai"
	DefaultBillingEndpoint           = "https://billing.usetelos.ai"
	DefaultTimeout                   = 30 * time.Second
	ForwardedUserAuthorizationHeader = "X-Telos-User-Authorization"
)

var supportedGatewayTransports = []string{
	string(gatewaycred.TransportBifrostAsync),
	string(gatewaycred.TransportOpenAISync),
}

// Environment describes a cloud Telos environment from the control plane.
type Environment struct {
	ID             string
	Handle         string
	AccessToken    string
	State          string
	HasRecoverable bool
}

// SessionKey is a budget-capped model gateway key minted by billing.
type SessionKey struct {
	SessionID string
	gatewaycred.Credential
	BudgetUSD float64
	KeyAlias  string
}

// Balance is the caller's current managed compute-unit balance.
type Balance struct {
	ComputeUnits float64
}

type OrganizationRecord struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type MeResponse struct {
	Subject       string               `json:"subject"`
	Email         string               `json:"email"`
	Name          string               `json:"name"`
	AuthMethod    string               `json:"auth_method"`
	IsAdmin       bool                 `json:"is_admin"`
	OrgID         string               `json:"org_id"`
	Organizations []OrganizationRecord `json:"organizations"`
}

type ApplyPackageRecord struct {
	Digest    string `json:"digest"`
	SizeBytes int    `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
}

type RegistryPackageRecord struct {
	Scope       string `json:"scope"`
	Name        string `json:"name"`
	Ref         string `json:"ref"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type RegistryVersionRecord struct {
	Scope     string `json:"scope"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Ref       string `json:"ref"`
	Digest    string `json:"digest"`
	CreatedAt string `json:"created_at"`
}

type DeploymentRecord struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	State          string `json:"state"`
	PackageRef     string `json:"package_ref"`
	PackageDigest  string `json:"package_digest"`
	RuntimeVersion string `json:"runtime_version"`
	ServiceURL     string `json:"service_url"`
	DashboardURL   string `json:"dashboard_url"`
	FailureReason  string `json:"failure_reason"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type CatalogSpecRecord struct {
	Name          string `json:"name"`
	PackageDigest string `json:"package_digest"`
	Visibility    string `json:"visibility"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type CatalogSpecPushResponse struct {
	Operation string            `json:"operation"`
	Spec      CatalogSpecRecord `json:"spec"`
}

type EnvironmentSessionRecord struct {
	EnvID         string `json:"env_id"`
	Name          string `json:"name"`
	PackageDigest string `json:"package_digest"`
	DesiredState  string `json:"desired_state"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type EnvironmentSessionApplyResponse struct {
	Operation string                   `json:"operation"`
	Session   EnvironmentSessionRecord `json:"session"`
}

type transport struct {
	endpoint           string
	token              string
	orgID              string
	forwardedUserToken string
	http               *http.Client
}

type ControlClient struct {
	transport *transport
}

type BillingClient struct {
	transport *transport
}

type EnvClient struct {
	transport *transport
}

// Client is kept as the environment-local Sessions API client type.
type Client = EnvClient

func newTransport(endpoint, token string) *transport {
	return &transport{
		endpoint: NormalizeEndpoint(endpoint),
		token:    token,
		http:     &http.Client{Timeout: DefaultTimeout},
	}
}

func NewControlClient(endpoint, token string) *ControlClient {
	return &ControlClient{transport: newTransport(endpoint, token)}
}

func NewBillingClient(endpoint, token string) *BillingClient {
	return &BillingClient{transport: newTransport(endpoint, token)}
}

func NewEnvClient(endpoint, token string) *EnvClient {
	return &EnvClient{transport: newTransport(endpoint, token)}
}

func (c *ControlClient) SetOrgID(orgID string) {
	if c != nil && c.transport != nil {
		c.transport.orgID = strings.TrimSpace(orgID)
	}
}

func (c *BillingClient) SetOrgID(orgID string) {
	if c != nil && c.transport != nil {
		c.transport.orgID = strings.TrimSpace(orgID)
	}
}

func (c *EnvClient) SetOrgID(orgID string) {
	if c != nil && c.transport != nil {
		c.transport.orgID = strings.TrimSpace(orgID)
	}
}

// NewClient creates an environment-local Sessions API client.
func NewClient(endpoint, token string) *EnvClient {
	return NewEnvClient(endpoint, token)
}

// NewEnvironmentAPIClient creates a client for an environment-local Sessions API.
func NewEnvironmentAPIClient(endpoint, token string) *EnvClient {
	cfg := config.LoadConfig()
	client := NewEnvClient(endpoint, token)
	client.transport.forwardedUserToken = cfg.AuthToken
	client.transport.orgID = cfg.OrgID
	return client
}

// NewControlClientFromConfig returns a client for the configured Telos control plane.
func NewControlClientFromConfig() (*ControlClient, error) {
	cfg := config.LoadConfig()
	endpoint := cfg.APIEndpoint
	if endpoint == "" {
		endpoint = DefaultAPIEndpoint
	}
	token := cfg.AuthToken
	if token == "" {
		return nil, fmt.Errorf("not logged in; run `telos login` first")
	}
	client := NewControlClient(endpoint, token)
	client.transport.orgID = cfg.OrgID
	return client, nil
}

// NewBillingClientFromConfig returns a client for the configured Telos billing service.
func NewBillingClientFromConfig() (*BillingClient, error) {
	cfg := config.LoadConfig()
	endpoint := cfg.BillingEndpoint
	if endpoint == "" {
		apiEndpoint := cfg.APIEndpoint
		if apiEndpoint == "" {
			apiEndpoint = DefaultAPIEndpoint
		}
		if NormalizeEndpoint(apiEndpoint) != DefaultAPIEndpoint {
			return nil, fmt.Errorf("billing_endpoint is required when api_endpoint is %s", apiEndpoint)
		}
		endpoint = DefaultBillingEndpoint
	}
	token := cfg.AuthToken
	if token == "" {
		return nil, fmt.Errorf("not logged in; run `telos login` first")
	}
	client := NewBillingClient(endpoint, token)
	client.transport.orgID = cfg.OrgID
	return client, nil
}

// NewClientFromConfig creates a client from the user's config file.
func NewClientFromConfig() (*ControlClient, error) {
	return NewControlClientFromConfig()
}

// NewEnvironmentClient resolves envID through the control plane and returns a
// client for that environment-local Sessions API.
func NewEnvironmentClient(envID string) (*EnvClient, *Environment, error) {
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
	return NewEnvironmentAPIClient("https://"+env.Handle, env.AccessToken), env, nil
}

// ResolveEnvironment returns the control-plane record plus local/recovered
// scoped access token for an owned environment.
func ResolveEnvironment(envID string) (*Environment, error) {
	control, err := NewControlClientFromConfig()
	if err != nil {
		return nil, err
	}
	env, err := control.GetEnvironment(envID)
	if err != nil {
		return nil, err
	}
	if access, ok := config.EnvironmentAccessByID(envID); ok {
		env.AccessToken = access.Token
		return env, nil
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

// CreateEnvironment creates a new cloud environment through the control plane.
func (c *ControlClient) CreateEnvironment() (*Environment, error) {
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

func (c *ControlClient) Me() (*MeResponse, error) {
	resp, err := c.do("GET", "/api/me", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var me MeResponse
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return nil, err
	}
	return &me, nil
}

// MintSessionKey asks billing to mint a managed per-session gateway key.
func (c *BillingClient) MintSessionKey(sessionID string, modelProfile gatewaycred.ModelProfile) (*SessionKey, error) {
	modelProfile, err := gatewaycred.NormalizeModelProfile(string(modelProfile))
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"session_id":           sessionID,
		"supported_transports": supportedGatewayTransports,
		"model_profile":        string(modelProfile),
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/billing/session-key", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var raw struct {
		SessionID string `json:"session_id"`
		gatewaycred.Credential
		BudgetUSD float64 `json:"budget_usd"`
		KeyAlias  string  `json:"key_alias"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if raw.BaseURL == "" || raw.APIKey == "" {
		return nil, fmt.Errorf("billing returned invalid session key")
	}
	raw.SessionID = strings.TrimSpace(raw.SessionID)
	if raw.SessionID == "" {
		return nil, fmt.Errorf("billing returned invalid session key: missing session_id")
	}
	if raw.SessionID != strings.TrimSpace(sessionID) {
		return nil, fmt.Errorf("billing returned session key for %q, want %q", raw.SessionID, strings.TrimSpace(sessionID))
	}
	if raw.Transport == "" {
		// Managed sessions default to the async transport; the native executor
		// drives it, unlike BYO gateways which stay openai_sync unless
		// configured otherwise.
		raw.Transport = gatewaycred.TransportBifrostAsync
	}
	if raw.ModelProfile == "" {
		raw.ModelProfile = modelProfile
	}
	cred, err := gatewaycred.Normalize(raw.Credential)
	if err != nil {
		return nil, err
	}
	return &SessionKey{
		SessionID:  raw.SessionID,
		Credential: cred,
		BudgetUSD:  raw.BudgetUSD,
		KeyAlias:   raw.KeyAlias,
	}, nil
}

// ReconcileSession asks the control plane to settle a managed local session.
func (c *BillingClient) ReconcileSession(sessionID string, terminal bool) error {
	path := "/api/billing/session-key/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/reconcile"
	if terminal {
		path += "?terminal=true"
	}
	resp, err := c.do("POST", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	return nil
}

func cloneStringMap(in map[string]string) map[string]string {
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

// Balance returns the caller's managed compute-unit balance.
func (c *BillingClient) Balance() (*Balance, error) {
	resp, err := c.do("GET", "/api/billing/balance", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var raw struct {
		ComputeUnits float64 `json:"compute_units"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return &Balance{ComputeUnits: raw.ComputeUnits}, nil
}

func (c *ControlClient) PublishRegistryVersion(scope, name, version string, data []byte) (*RegistryVersionRecord, error) {
	path := "/api/packages/" + url.PathEscape(scope) + "/" + url.PathEscape(name) + "/versions/" + url.PathEscape(version)
	resp, err := c.doRaw("PUT", path, data, "application/gzip")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var record RegistryVersionRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *ControlClient) ListDeployments() ([]DeploymentRecord, error) {
	resp, err := c.do("GET", "/api/deployments", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var payload struct {
		Deployments []DeploymentRecord `json:"deployments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Deployments, nil
}

func (c *ControlClient) CreateDeployment(name, packageRef, runtimeVersion string) (*DeploymentRecord, error) {
	body := map[string]string{"package_ref": packageRef}
	if strings.TrimSpace(name) != "" {
		body["name"] = strings.TrimSpace(name)
	}
	if strings.TrimSpace(runtimeVersion) != "" {
		body["runtime_version"] = strings.TrimSpace(runtimeVersion)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/deployments", data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var record DeploymentRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *ControlClient) GetDeployment(id string) (*DeploymentRecord, error) {
	return c.getDeployment(context.Background(), id)
}

func (c *ControlClient) getDeployment(ctx context.Context, id string) (*DeploymentRecord, error) {
	resp, err := c.doContext(ctx, "GET", "/api/deployments/"+url.PathEscape(strings.TrimSpace(id)), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var record DeploymentRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *ControlClient) UpdateDeployment(id, packageRef, runtimeVersion string) (*DeploymentRecord, error) {
	body := map[string]string{"package_ref": packageRef}
	if strings.TrimSpace(runtimeVersion) != "" {
		body["runtime_version"] = strings.TrimSpace(runtimeVersion)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := c.do("PUT", "/api/deployments/"+url.PathEscape(strings.TrimSpace(id)), data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var record DeploymentRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *ControlClient) DeleteDeployment(id string) (*DeploymentRecord, error) {
	resp, err := c.do("DELETE", "/api/deployments/"+url.PathEscape(strings.TrimSpace(id)), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var record DeploymentRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *ControlClient) GetDeploymentTranscript(id string) (string, error) {
	resp, err := c.do("GET", "/api/deployments/"+url.PathEscape(strings.TrimSpace(id))+"/transcript", nil)
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

func (c *ControlClient) WaitForDeployment(ctx context.Context, id string, timeout time.Duration) (*DeploymentRecord, error) {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var last *DeploymentRecord
	for {
		record, err := c.getDeployment(ctx, id)
		if err != nil {
			return nil, err
		}
		last = record
		switch record.State {
		case "healthy":
			return record, nil
		case "failed", "deleted":
			return record, fmt.Errorf("deployment %s is %s: %s", id, record.State, strings.TrimSpace(record.FailureReason))
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("deployment %s did not become healthy before timeout", id)
		case <-ticker.C:
		}
	}
}

func (c *ControlClient) UploadApplyPackage(digest string, data []byte) (*ApplyPackageRecord, error) {
	resp, err := c.doRaw("PUT", "/api/catalog/packages/"+url.PathEscape(digest), data, "application/gzip")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var record ApplyPackageRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *ControlClient) PushCatalogSpec(name string, packageDigest string) (*CatalogSpecPushResponse, error) {
	body, err := json.Marshal(map[string]string{"package_digest": packageDigest})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("PUT", "/api/catalog/specs/"+url.PathEscape(name), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response CatalogSpecPushResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *ControlClient) ApplyEnvironmentSession(envID, name, packageDigest string) (*EnvironmentSessionApplyResponse, error) {
	body, err := json.Marshal(map[string]string{"package_digest": packageDigest})
	if err != nil {
		return nil, err
	}
	resp, err := c.do("PUT", "/api/environments/"+url.PathEscape(envID)+"/sessions/"+url.PathEscape(name), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response EnvironmentSessionApplyResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return &response, nil
}

// WaitForEnvironment waits until an environment-local API is reachable.
func WaitForEnvironment(ctx context.Context, handle string, timeout time.Duration) error {
	client := &http.Client{Timeout: 5 * time.Second}
	return waitForEnvironment(ctx, handle, timeout, client, 5*time.Second)
}

func waitForEnvironment(ctx context.Context, handle string, timeout time.Duration, client *http.Client, pollInterval time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	url := NormalizeEndpoint(handle) + "/api/healthz"
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil && resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			if ctx.Err() == nil || lastErr == nil {
				lastErr = err
			}
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("%s did not become ready: %w", handle, lastErr)
			}
			return fmt.Errorf("%s did not become ready", handle)
		case <-ticker.C:
		}
	}
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
func (c *EnvClient) CreateSession(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
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
func (c *EnvClient) ApplySessionSpec(name string, req sessionapi.SessionSpecUpdateRequest) (*sessionapi.SessionSpecUpdateResponse, error) {
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

// ListEnvironments lists cloud environments from the control plane.
func (c *ControlClient) ListEnvironments() ([]Environment, error) {
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

// GetEnvironment resolves an environment control-plane record without requiring
// or issuing an environment-local access token.
func (c *ControlClient) GetEnvironment(envID string) (*Environment, error) {
	if envID == "" {
		return nil, fmt.Errorf("environment id is required")
	}
	envs, err := c.ListEnvironments()
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		if env.ID == envID {
			return &env, nil
		}
	}
	return nil, fmt.Errorf("environment %s not found", envID)
}

// IssueEnvironmentAccess issues a scoped environment access token.
func (c *ControlClient) IssueEnvironmentAccess(envID string) (*Environment, error) {
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
func (c *EnvClient) ListSessions(limit int, includeChildren bool) ([]sessionapi.Session, error) {
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
func (c *EnvClient) GetSession(id string) (*sessionapi.Session, error) {
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
func (c *EnvClient) StopSession(id string) (*sessionapi.Session, error) {
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
func (c *EnvClient) GetTranscript(id string) (string, error) {
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
func (c *EnvClient) GetEvents(id string) ([]sessionapi.SessionEvent, error) {
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

// GetDiagnostics gets the production inspection payload for a session.
func (c *EnvClient) GetDiagnostics(id string) (*sessionapi.SessionDiagnosticsResponse, error) {
	resp, err := c.do("GET", "/api/sessions/"+id+"/diagnostics", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var diagnostics sessionapi.SessionDiagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&diagnostics); err != nil {
		return nil, err
	}
	return &diagnostics, nil
}

// StreamEvents follows the cloud event stream for a session.
func (c *EnvClient) StreamEvents(ctx context.Context, id string, onEvent func(map[string]any) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.transport.endpoint+"/api/sessions/"+id+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.transport.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.transport.token)
	}
	if strings.TrimSpace(c.transport.orgID) != "" {
		req.Header.Set("X-Telos-Org-Id", strings.TrimSpace(c.transport.orgID))
	}
	if c.transport.forwardedUserToken != "" {
		req.Header.Set(ForwardedUserAuthorizationHeader, "Bearer "+c.transport.forwardedUserToken)
	}
	client := http.DefaultClient
	if c.transport.http != nil {
		clone := *c.transport.http
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

func (c *ControlClient) do(method, path string, body []byte) (*http.Response, error) {
	return c.doContext(context.Background(), method, path, body)
}

func (c *ControlClient) doContext(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	return c.transport.doRaw(ctx, method, path, body, "application/json", false)
}

func (c *ControlClient) doRaw(method, path string, body []byte, contentType string) (*http.Response, error) {
	return c.transport.doRaw(context.Background(), method, path, body, contentType, false)
}

func (c *BillingClient) do(method, path string, body []byte) (*http.Response, error) {
	return c.transport.doRaw(context.Background(), method, path, body, "application/json", false)
}

func (c *EnvClient) do(method, path string, body []byte) (*http.Response, error) {
	return c.transport.doRaw(context.Background(), method, path, body, "application/json", true)
}

func (t *transport) doRaw(ctx context.Context, method, path string, body []byte, contentType string, forwardUser bool) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.endpoint+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	if strings.TrimSpace(t.orgID) != "" {
		req.Header.Set("X-Telos-Org-Id", strings.TrimSpace(t.orgID))
	}
	if forwardUser && t.forwardedUserToken != "" {
		req.Header.Set(ForwardedUserAuthorizationHeader, "Bearer "+t.forwardedUserToken)
	}
	return t.http.Do(req)
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
