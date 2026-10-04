package cloud

import (
	"encoding/json"
	"net/http"
)

// SecretRecord deliberately omits credential values, policy configuration and
// environment metadata from the Cloud response.
type SecretRecord struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Credentials []SecretCredentialKey `json:"credentials"`
	ManagedBy   *string               `json:"managed_by,omitempty"`
}

type SecretCredentialKey struct {
	Key string `json:"key"`
}

func (c *Client) ListSecrets() ([]SecretRecord, error) {
	resp, err := c.do(http.MethodGet, "/api/secrets", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var response struct {
		Secrets []SecretRecord `json:"secrets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response.Secrets, nil
}
