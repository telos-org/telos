// Package cloud provides the Telos Cloud API client.
package cloud

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/telos-org/telos/internal/bundlelimits"
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

type PackageRecord struct {
	Scope         string                 `json:"scope"`
	Name          string                 `json:"name"`
	Ref           string                 `json:"ref"`
	DisplayName   *string                `json:"display_name,omitempty"`
	Description   *string                `json:"description,omitempty"`
	Visibility    string                 `json:"visibility"`
	Tags          []string               `json:"tags"`
	LatestVersion *PackageVersionRecord  `json:"latest_version,omitempty"`
	Versions      []PackageVersionRecord `json:"versions,omitempty"`
	CreatedAt     string                 `json:"created_at"`
	UpdatedAt     string                 `json:"updated_at"`
	CanManage     bool                   `json:"can_manage"`
}

type RegistryVisibilityBlocker struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Ref     *string `json:"ref,omitempty"`
}

type RegistryVisibilityPreflight struct {
	ArtifactKind      string                      `json:"artifact_kind"`
	Scope             string                      `json:"scope"`
	Name              string                      `json:"name"`
	CurrentVisibility string                      `json:"current_visibility"`
	TargetVisibility  string                      `json:"target_visibility"`
	IdentityRevision  int                         `json:"identity_revision"`
	VersionCount      int                         `json:"version_count"`
	VersionSetDigest  string                      `json:"version_set_digest"`
	ConfirmationToken string                      `json:"confirmation_token"`
	Warning           *string                     `json:"warning,omitempty"`
	Blockers          []RegistryVisibilityBlocker `json:"blockers"`
}

type Capabilities struct {
	DeploymentRevisionHistory  bool `json:"deployment_revision_history"`
	DeploymentRevisionMessages bool `json:"deployment_revision_messages"`
	DeploymentPackageRedeploy  bool `json:"deployment_package_redeploy"`
	DeploymentSnapshotRestore  bool `json:"deployment_snapshot_restore"`
	RegistryPrivacy            bool `json:"registry_privacy"`
}

type SkillFile struct {
	Mode string
	Data []byte
}

type SkillRecord struct {
	Scope       string         `json:"scope"`
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Ref         string         `json:"ref"`
	Digest      string         `json:"digest"`
	Description *string        `json:"description,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	FileCount   int            `json:"file_count"`
	SourceRef   string         `json:"source_ref"`
	Visibility  string         `json:"visibility"`
	CanManage   bool           `json:"can_manage"`
}

type packageListResponse struct {
	Packages []PackageRecord `json:"packages"`
}

type skillListResponse struct {
	Skills []SkillRecord `json:"skills"`
}

type SessionRecord struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	State          string  `json:"state"`
	Status         string  `json:"status,omitempty"`
	StatusReason   string  `json:"status_reason,omitempty"`
	PackageRef     string  `json:"package_ref"`
	PackageDigest  string  `json:"package_digest"`
	RuntimeVersion *string `json:"runtime_version,omitempty"`
	AgentModel     string  `json:"agent_model,omitempty"`
	AgentThinking  string  `json:"agent_thinking,omitempty"`
	ServiceURL     *string `json:"service_url,omitempty"`
	DashboardURL   *string `json:"dashboard_url,omitempty"`
	FailureReason  *string `json:"failure_reason,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

type SessionCreateOptions struct {
	Name            string
	PackageRef      string
	AgentModel      string
	AgentThinking   string
	AgentTimeoutSec *int
}

// The hosted control API still exposes cloud sessions at /api/deployments.
// Keep that wire contract here and expose session-shaped methods to the CLI.
type sessionListResponse struct {
	Sessions []SessionRecord `json:"deployments"`
}

type deploymentLogEventsResponse struct {
	Events []json.RawMessage `json:"events"`
}

// SessionLogPage keeps both the normalized events used by the human/JSON
// views and their original wire records for --raw output.
type SessionLogPage struct {
	Events    []sessionapi.SessionEvent
	RawEvents []json.RawMessage
}

type deploymentLogEvent struct {
	Schema           *string        `json:"schema,omitempty"`
	EventID          *string        `json:"event_id,omitempty"`
	Event            string         `json:"event"`
	EventSeq         *int64         `json:"event_seq,omitempty"`
	EpochID          *int           `json:"epoch_id,omitempty"`
	Round            *int           `json:"round,omitempty"`
	Role             *string        `json:"role,omitempty"`
	Timestamp        *string        `json:"ts,omitempty"`
	SourceTimestamp  *string        `json:"source_ts,omitempty"`
	ReceivedAt       *string        `json:"received_at,omitempty"`
	Time             *string        `json:"time,omitempty"`
	SessionStartedAt *string        `json:"session_started_at,omitempty"`
	SessionID        *string        `json:"session_id,omitempty"`
	Source           *string        `json:"source,omitempty"`
	System           *string        `json:"system,omitempty"`
	SpecIndex        *int           `json:"spec_index,omitempty"`
	SpecName         *string        `json:"spec_name,omitempty"`
	SpecDirName      *string        `json:"spec_dir_name,omitempty"`
	Data             map[string]any `json:"data,omitempty"`
	Message          string         `json:"message,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
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
	receivedAt := event.ReceivedAt
	if receivedAt == nil {
		receivedAt = event.Time
	}
	timestamp := receivedAt
	if timestamp == nil {
		timestamp = event.Timestamp
	}
	sourceTimestamp := event.SourceTimestamp
	if sourceTimestamp == nil && receivedAt != nil {
		sourceTimestamp = event.Timestamp
	}
	return sessionapi.SessionEvent{
		Schema:           event.Schema,
		EventID:          event.EventID,
		Event:            event.Event,
		EventSeq:         event.EventSeq,
		EpochID:          event.EpochID,
		Round:            event.Round,
		Role:             event.Role,
		Timestamp:        timestamp,
		SourceTimestamp:  sourceTimestamp,
		ReceivedAt:       receivedAt,
		SessionStartedAt: event.SessionStartedAt,
		SessionID:        event.SessionID,
		Source:           event.Source,
		System:           event.System,
		SpecIndex:        event.SpecIndex,
		SpecName:         event.SpecName,
		SpecDirName:      event.SpecDirName,
		Data:             data,
	}
}

// Client is a Telos Cloud API client.
type Client struct {
	Endpoint string
	Token    string
	OrgID    string
	HTTP     *http.Client
}

type APIError struct {
	StatusCode int
	Code       string
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

// resolvedContexts memoizes successful handle resolutions for the life of
// the process, keyed by endpoint, token, and context so distinct identities
// never share an entry. Failures are not cached so transient errors stay
// retryable.
var resolvedContexts sync.Map

// ControlClient returns a client for the configured Telos control plane.
func ControlClient() (*Client, error) {
	return ControlClientForContext("")
}

// ControlClientForContext returns a configured client, optionally overriding
// the environment or stored context for this client only.
func ControlClientForContext(contextOverride string) (*Client, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, err
	}
	endpoint := cfg.APIEndpoint
	if endpoint == "" {
		endpoint = DefaultAPIEndpoint
	}
	token := cfg.AuthToken
	if token == "" {
		return nil, fmt.Errorf("not logged in; run `telos login` first")
	}
	client := NewClient(endpoint, token)
	context := strings.TrimSpace(contextOverride)
	if context == "" {
		context = strings.TrimSpace(cfg.Context)
	}
	if context == "" || context == "personal" {
		return client, nil
	}
	if strings.HasPrefix(context, "org_") {
		client.OrgID = context
		return client, nil
	}
	key := client.Endpoint + "\x00" + token + "\x00" + context
	if orgID, ok := resolvedContexts.Load(key); ok {
		client.OrgID = orgID.(string)
		return client, nil
	}
	organization, err := client.ResolveContext(context)
	if err != nil {
		return nil, err
	}
	resolvedContexts.Store(key, organization.ID)
	client.OrgID = organization.ID
	return client, nil
}

// RegistryReadClientForContext returns an authenticated Registry client when
// credentials are configured and otherwise returns an anonymous client for
// public Registry reads. An explicitly selected organization always requires
// authentication.
func RegistryReadClientForContext(contextOverride string) (*Client, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.AuthToken) != "" {
		return ControlClientForContext(contextOverride)
	}
	context := strings.TrimSpace(contextOverride)
	if context != "" && context != "personal" {
		return nil, fmt.Errorf("--context requires login; run `telos login` first")
	}
	endpoint := cfg.APIEndpoint
	if endpoint == "" {
		endpoint = DefaultAPIEndpoint
	}
	return NewClient(endpoint, ""), nil
}

// NewClientFromConfig creates a client from the user's config file.
func NewClientFromConfig() (*Client, error) {
	return ControlClient()
}

func (c *Client) PublishPackage(scope, name, version string, data []byte) (*PackageVersionRecord, error) {
	return c.PublishPackageWithVisibility(scope, name, version, data, "")
}

func (c *Client) PublishPackageWithVisibility(
	scope string,
	name string,
	version string,
	data []byte,
	visibility string,
) (*PackageVersionRecord, error) {
	payload := map[string]any{
		"name":        name,
		"data_base64": base64.StdEncoding.EncodeToString(data),
	}
	if strings.TrimSpace(scope) != "" {
		payload["scope"] = scope
	}
	if strings.TrimSpace(version) != "" {
		payload["version"] = version
	}
	if strings.TrimSpace(visibility) != "" {
		payload["visibility"] = strings.TrimSpace(visibility)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/packages", body)
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

func (c *Client) CustomizePackage(
	sourceScope string,
	sourceName string,
	sourceVersion string,
	targetScope string,
	targetName string,
	targetVersion string,
	data []byte,
) (*PackageVersionRecord, error) {
	payload := map[string]any{
		"name":        targetName,
		"data_base64": base64.StdEncoding.EncodeToString(data),
	}
	if strings.TrimSpace(targetScope) != "" {
		payload["scope"] = strings.TrimSpace(targetScope)
	}
	if strings.TrimSpace(targetVersion) != "" {
		payload["version"] = strings.TrimSpace(targetVersion)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	path := "/api/packages/" +
		url.PathEscape(strings.TrimSpace(sourceScope)) + "/" +
		url.PathEscape(strings.TrimSpace(sourceName)) + "/versions/" +
		url.PathEscape(strings.TrimSpace(sourceVersion)) + "/customize"
	resp, err := c.do("POST", path, body)
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

func (c *Client) ListPackages() ([]PackageRecord, error) {
	resp, err := c.do("GET", "/api/packages", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response packageListResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response.Packages, nil
}

func (c *Client) GetPackage(scope, name string) (*PackageRecord, error) {
	path := "/api/packages/" +
		url.PathEscape(strings.TrimSpace(scope)) + "/" +
		url.PathEscape(strings.TrimSpace(name))
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var record PackageRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) GetPackageVersion(scope, name, version string) (*PackageVersionRecord, error) {
	path := "/api/packages/" +
		url.PathEscape(strings.TrimSpace(scope)) + "/" +
		url.PathEscape(strings.TrimSpace(name)) + "/versions/" +
		url.PathEscape(strings.TrimSpace(version))
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var record PackageVersionRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) DownloadPackageVersionBundle(scope, name, version string) ([]byte, error) {
	path := "/api/packages/" +
		url.PathEscape(strings.TrimSpace(scope)) + "/" +
		url.PathEscape(strings.TrimSpace(name)) + "/versions/" +
		url.PathEscape(strings.TrimSpace(version)) + "/bundle"
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	return readBundleResponse(resp, "package bundle")
}

func (c *Client) PublishSkillVersion(scope, name, version string, files map[string]SkillFile) (*SkillRecord, error) {
	return c.PublishSkillVersionWithVisibility(scope, name, version, files, "")
}

func (c *Client) PublishSkillVersionWithVisibility(
	scope string,
	name string,
	version string,
	files map[string]SkillFile,
	visibility string,
) (*SkillRecord, error) {
	type skillFileRequest struct {
		DataBase64 string `json:"data_base64"`
		Mode       string `json:"mode"`
	}
	bodyFiles := make(map[string]skillFileRequest, len(files))
	for path, file := range files {
		bodyFiles[path] = skillFileRequest{
			DataBase64: base64.StdEncoding.EncodeToString(file.Data),
			Mode:       file.Mode,
		}
	}
	payload := map[string]any{
		"name":  name,
		"files": bodyFiles,
	}
	if strings.TrimSpace(scope) != "" {
		payload["scope"] = scope
	}
	if strings.TrimSpace(version) != "" {
		payload["version"] = version
	}
	if strings.TrimSpace(visibility) != "" {
		payload["visibility"] = strings.TrimSpace(visibility)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.do("POST", "/api/skills", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var record SkillRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) CustomizeSkillVersion(
	sourceScope string,
	sourceName string,
	sourceVersion string,
	targetScope string,
	targetName string,
	targetVersion string,
	files map[string]SkillFile,
) (*SkillRecord, error) {
	type skillFileRequest struct {
		DataBase64 string `json:"data_base64"`
		Mode       string `json:"mode"`
	}
	bodyFiles := make(map[string]skillFileRequest, len(files))
	for path, file := range files {
		bodyFiles[path] = skillFileRequest{
			DataBase64: base64.StdEncoding.EncodeToString(file.Data),
			Mode:       file.Mode,
		}
	}
	payload := map[string]any{
		"name":  targetName,
		"files": bodyFiles,
	}
	if strings.TrimSpace(targetScope) != "" {
		payload["scope"] = strings.TrimSpace(targetScope)
	}
	if strings.TrimSpace(targetVersion) != "" {
		payload["version"] = strings.TrimSpace(targetVersion)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	path := "/api/skills/" +
		url.PathEscape(strings.TrimSpace(sourceScope)) + "/" +
		url.PathEscape(strings.TrimSpace(sourceName)) + "/versions/" +
		url.PathEscape(strings.TrimSpace(sourceVersion)) + "/customize"
	resp, err := c.do("POST", path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readError(resp)
	}
	var record SkillRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) ListSkills() ([]SkillRecord, error) {
	resp, err := c.do("GET", "/api/skills", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response skillListResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response.Skills, nil
}

func (c *Client) GetSkill(scope, name string) (*SkillRecord, error) {
	path := "/api/skills/" + url.PathEscape(strings.TrimSpace(scope)) + "/" + url.PathEscape(strings.TrimSpace(name))
	return c.getSkill(path)
}

func (c *Client) GetSkillVersion(scope, name, version string) (*SkillRecord, error) {
	path := "/api/skills/" +
		url.PathEscape(strings.TrimSpace(scope)) + "/" +
		url.PathEscape(strings.TrimSpace(name)) + "/versions/" +
		url.PathEscape(strings.TrimSpace(version))
	return c.getSkill(path)
}

func (c *Client) DownloadSkillVersionBundle(scope, name, version string) ([]byte, error) {
	path := "/api/skills/" +
		url.PathEscape(strings.TrimSpace(scope)) + "/" +
		url.PathEscape(strings.TrimSpace(name)) + "/versions/" +
		url.PathEscape(strings.TrimSpace(version)) + "/bundle"
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	return readBundleResponse(resp, "skill bundle")
}

func readBundleResponse(resp *http.Response, label string) ([]byte, error) {
	return readBundleResponseWithLimit(resp, label, bundlelimits.MaxCompressedBytes)
}

func readBundleResponseWithLimit(resp *http.Response, label string, maxBytes int64) ([]byte, error) {
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return data, nil
}

func (c *Client) getSkill(path string) (*SkillRecord, error) {
	resp, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var record SkillRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) RegistryCapabilities() (*Capabilities, error) {
	resp, err := c.do("GET", "/api/capabilities", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var capabilities Capabilities
	if err := json.NewDecoder(resp.Body).Decode(&capabilities); err != nil {
		return nil, err
	}
	return &capabilities, nil
}

func (c *Client) PreflightPackageVisibility(
	scope string,
	name string,
	visibility string,
) (*RegistryVisibilityPreflight, error) {
	return c.preflightRegistryVisibility("packages", scope, name, visibility)
}

func (c *Client) PreflightSkillVisibility(
	scope string,
	name string,
	visibility string,
) (*RegistryVisibilityPreflight, error) {
	return c.preflightRegistryVisibility("skills", scope, name, visibility)
}

func (c *Client) preflightRegistryVisibility(
	kind string,
	scope string,
	name string,
	visibility string,
) (*RegistryVisibilityPreflight, error) {
	body, err := json.Marshal(map[string]string{
		"visibility": strings.TrimSpace(visibility),
	})
	if err != nil {
		return nil, err
	}
	path := "/api/" + kind + "/" +
		url.PathEscape(strings.TrimSpace(scope)) + "/" +
		url.PathEscape(strings.TrimSpace(name)) + "/visibility/preflight"
	resp, err := c.do("POST", path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var preflight RegistryVisibilityPreflight
	if err := json.NewDecoder(resp.Body).Decode(&preflight); err != nil {
		return nil, err
	}
	return &preflight, nil
}

func (c *Client) ChangePackageVisibility(
	scope string,
	name string,
	visibility string,
	confirmationToken string,
) (*PackageRecord, error) {
	body, err := registryVisibilityApplyBody(visibility, confirmationToken)
	if err != nil {
		return nil, err
	}
	path := "/api/packages/" +
		url.PathEscape(strings.TrimSpace(scope)) + "/" +
		url.PathEscape(strings.TrimSpace(name)) + "/visibility"
	resp, err := c.do("PUT", path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var record PackageRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) ChangeSkillVisibility(
	scope string,
	name string,
	visibility string,
	confirmationToken string,
) (*SkillRecord, error) {
	body, err := registryVisibilityApplyBody(visibility, confirmationToken)
	if err != nil {
		return nil, err
	}
	path := "/api/skills/" +
		url.PathEscape(strings.TrimSpace(scope)) + "/" +
		url.PathEscape(strings.TrimSpace(name)) + "/visibility"
	resp, err := c.do("PUT", path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var record SkillRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func registryVisibilityApplyBody(visibility, confirmationToken string) ([]byte, error) {
	return json.Marshal(map[string]string{
		"visibility":         strings.TrimSpace(visibility),
		"confirmation_token": strings.TrimSpace(confirmationToken),
	})
}

func (c *Client) CreateSession(opts SessionCreateOptions) (*SessionRecord, error) {
	payload := map[string]any{
		"name":        opts.Name,
		"package_ref": opts.PackageRef,
	}
	if strings.TrimSpace(opts.AgentModel) != "" {
		payload["agent_model"] = strings.TrimSpace(opts.AgentModel)
	}
	if strings.TrimSpace(opts.AgentThinking) != "" {
		payload["agent_thinking"] = strings.TrimSpace(opts.AgentThinking)
	}
	if opts.AgentTimeoutSec != nil {
		payload["agent_timeout_sec"] = *opts.AgentTimeoutSec
	}
	body, err := json.Marshal(payload)
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

func (c *Client) GetSessionLogs(sessionID string) ([]sessionapi.SessionEvent, error) {
	page, err := c.GetSessionLogPage(sessionID)
	if err != nil {
		return nil, err
	}
	return page.Events, nil
}

func (c *Client) GetSessionLogPage(sessionID string) (*SessionLogPage, error) {
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
	for _, raw := range response.Events {
		var event deploymentLogEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("decode session log event: %w", err)
		}
		events = append(events, event.asSessionEvent())
	}
	return &SessionLogPage{
		Events:    events,
		RawEvents: response.Events,
	}, nil
}

// NormalizeEndpoint cleans up an API endpoint URL.
func NormalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = "https://" + endpoint
	}
	return endpoint
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
	if strings.TrimSpace(c.OrgID) != "" {
		req.Header.Set("X-Telos-Org-Id", strings.TrimSpace(c.OrgID))
	}
	return c.HTTP.Do(req)
}

func readError(resp *http.Response) error {
	data, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		if rawError, ok := m["error"].(map[string]any); ok {
			code, _ := rawError["code"].(string)
			message, _ := rawError["message"].(string)
			if code != "" || message != "" {
				return &APIError{
					StatusCode: resp.StatusCode,
					Code:       code,
					Detail:     message,
				}
			}
		}
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
