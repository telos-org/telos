package cloud

// Credential is a saved workspace credential, which a Goal's network rules name
// by Name. Cloud never returns its values.
type Credential struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListCredentials returns the context's saved credentials, leaving out the ones
// Telos manages for itself.
func (c *Client) ListCredentials() ([]Credential, error) {
	result, err := getJSONWithRetry[struct {
		Secrets []struct {
			Credential
			ManagedBy *string `json:"managed_by"`
		} `json:"secrets"`
	}](c.context(), c, "/api/secrets")
	if err != nil {
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
