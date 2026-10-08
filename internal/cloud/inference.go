package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

type InferenceConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Account  string `json:"account_label,omitempty"`
}

type InferenceSelection struct {
	Source       string `json:"source"`
	Tier         string `json:"tier,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	Model        string `json:"model,omitempty"`
}

type InferenceSummary struct {
	Source         string `json:"source"`
	Tier           string `json:"tier,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	ConnectionName string `json:"connection_name,omitempty"`
}

type DeploymentInferenceRequest struct {
	ApplyAt          string              `json:"apply_at"`
	RequestID        string              `json:"request_id"`
	ExpectedRevision int                 `json:"expected_revision"`
	Inference        *InferenceSelection `json:"inference,omitempty"`
	AgentThinking    *string             `json:"agent_thinking,omitempty"`
}

type DeploymentInferenceState struct {
	Inference     InferenceSummary            `json:"inference"`
	AgentModel    string                      `json:"agent_model"`
	AgentThinking string                      `json:"agent_thinking"`
	Revision      int                         `json:"revision"`
	Request       *DeploymentInferenceRequest `json:"request,omitempty"`
	Status        string                      `json:"status,omitempty"`
	Error         string                      `json:"error,omitempty"`
}

func (c *Client) GetDeploymentInference(id string) (*DeploymentInferenceState, error) {
	return getJSONWithRetry[DeploymentInferenceState](context.Background(), c, "/api/deployments/"+url.PathEscape(id)+"/inference")
}

// Writes are single-attempt: a lost response may follow a saved change.
func (c *Client) UpdateDeploymentInference(id string, request DeploymentInferenceRequest) (*DeploymentInferenceState, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(http.MethodPut, "/api/deployments/"+url.PathEscape(id)+"/inference", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, readError(resp)
	}
	var state DeploymentInferenceState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (c *Client) ListInferenceConnections() ([]InferenceConnection, error) {
	var result struct {
		Connections []InferenceConnection `json:"connections"`
		Errors      map[string]string     `json:"errors"`
	}
	if err := c.inferenceJSON("/api/inference/connections", &result); err != nil {
		return nil, err
	}
	if result.Errors == nil {
		return nil, fmt.Errorf("Cloud does not support unified inference discovery; update Cloud before selecting a saved API key or subscription")
	}
	var failures []error
	for source, message := range result.Errors {
		failures = append(failures, fmt.Errorf("%s: %s", source, message))
	}
	return result.Connections, errors.Join(failures...)
}

func (c *Client) InferencePreference() (*InferenceSelection, error) {
	var result struct {
		Selection InferenceSelection `json:"selection"`
	}
	if err := c.inferenceJSON("/api/inference/preference", &result); err != nil {
		return nil, err
	}
	return &result.Selection, nil
}

func (c *Client) inferenceJSON(path string, result any) error {
	resp, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(result)
}
