package cloud

import (
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

type InferencePreference struct {
	Selection     InferenceSelection `json:"selection"`
	AgentThinking *string            `json:"agent_thinking"`
}

type InferenceSummary struct {
	Source         string `json:"source"`
	Tier           string `json:"tier,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	ConnectionName string `json:"connection_name,omitempty"`
}

// DeploymentInference is the deployment's desired settings. Applied reports
// whether the Goal's runtime has stored them for its next turn.
type DeploymentInference struct {
	Inference     InferenceSummary    `json:"inference"`
	Selection     *InferenceSelection `json:"selection,omitempty"`
	AgentModel    string              `json:"agent_model"`
	AgentThinking string              `json:"agent_thinking"`
	Generation    int                 `json:"generation"`
	Applied       bool                `json:"applied"`
}

func (c *Client) GetDeploymentInference(id string) (*DeploymentInference, error) {
	var state DeploymentInference
	if err := c.inferenceJSON("/api/deployments/"+url.PathEscape(id)+"/inference", &state); err != nil {
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

func (c *Client) InferencePreference() (*InferencePreference, error) {
	var result InferencePreference
	if err := c.inferenceJSON("/api/inference/preference", &result); err != nil {
		return nil, err
	}
	return &result, nil
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
