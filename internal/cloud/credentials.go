package cloud

// Credential is a saved workspace credential that a Goal's network rules can
// name by ID. Cloud never returns its values.
type Credential struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListCredentials returns the context's saved credentials, leaving out the ones
// Telos manages for itself.
func (c *Client) ListCredentials() ([]Credential, error) {
	var result struct {
		Secrets []struct {
			Credential
			ManagedBy *string `json:"managed_by"`
		} `json:"secrets"`
	}
	if err := c.getJSON("/api/secrets", &result); err != nil {
		return nil, err
	}
	credentials := []Credential{}
	for _, secret := range result.Secrets {
		if secret.ManagedBy == nil {
			credentials = append(credentials, secret.Credential)
		}
	}
	return credentials, nil
}
