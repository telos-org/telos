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

func (c *Client) ListSubscriptionConnections() ([]SubscriptionConnection, error) {
	resp, err := c.do(http.MethodGet, "/api/inference/connections", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var result subscriptionConnectionList
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Connections, nil
}
