package cloud

import (
	"encoding/json"
	"net/http"
)

type SubscriptionConnection struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Provider     string  `json:"provider"`
	Status       string  `json:"status"`
	AccountLabel *string `json:"account_label"`
	Plan         *string `json:"plan"`
}

type subscriptionConnectionList struct {
	Connections []SubscriptionConnection `json:"connections"`
}

type InferenceSelection struct {
	Source       string `json:"source"`
	Tier         string `json:"tier,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	Model        string `json:"model,omitempty"`
}

type APIKeyConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type InferenceModel struct {
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	ConnectionIDs []string `json:"connection_ids,omitempty"`
}

type ConnectionCatalog struct {
	ConnectionID string           `json:"connection_id"`
	Models       []InferenceModel `json:"models"`
	Error        *string          `json:"error"`
}

type APIKeyCatalog struct {
	Enabled     bool                `json:"enabled"`
	Connections []ConnectionCatalog `json:"connections"`
}

type InferencePreference struct {
	Selection InferenceSelection `json:"selection"`
}

type InferenceSummary struct {
	Source         string `json:"source"`
	Tier           string `json:"tier,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	ConnectionName string `json:"connection_name,omitempty"`
}

func (c *Client) ListSubscriptionConnections() ([]SubscriptionConnection, error) {
	var result subscriptionConnectionList
	err := c.inferenceJSON("/api/inference/connections", &result)
	return result.Connections, err
}

func (c *Client) ListAPIKeyConnections() ([]APIKeyConnection, error) {
	var result struct {
		Connections []APIKeyConnection `json:"connections"`
	}
	err := c.inferenceJSON("/api/inference/api-keys", &result)
	return result.Connections, err
}

func (c *Client) SubscriptionCatalog() ([]InferenceModel, error) {
	var result struct {
		Models []InferenceModel `json:"models"`
	}
	err := c.inferenceJSON("/api/inference/catalog", &result)
	return result.Models, err
}

func (c *Client) APIKeyCatalog() (*APIKeyCatalog, error) {
	var result APIKeyCatalog
	err := c.inferenceJSON("/api/inference/api-keys/catalog", &result)
	return &result, err
}

func (c *Client) InferencePreference() (*InferencePreference, error) {
	var result InferencePreference
	err := c.inferenceJSON("/api/inference/preference", &result)
	return &result, err
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
