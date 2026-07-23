package cloud

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type OrganizationRecord struct {
	ID                  string  `json:"id"`
	Handle              *string `json:"handle"`
	DisplayName         string  `json:"display_name"`
	Kind                string  `json:"kind"`
	Role                string  `json:"role"`
	DefaultPublishScope *string `json:"default_publish_scope"`
}

type AccountBootstrapRecord struct {
	PersonalOrgID string               `json:"personal_org_id"`
	Organizations []OrganizationRecord `json:"organizations"`
}

func (c *Client) AccountBootstrap() (*AccountBootstrapRecord, error) {
	resp, err := c.do("GET", "/api/account/bootstrap", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var account AccountBootstrapRecord
	if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
		return nil, err
	}
	return &account, nil
}

func (c *Client) ResolveContext(value string) (*OrganizationRecord, error) {
	account, err := c.AccountBootstrap()
	if err != nil {
		return nil, err
	}
	return account.ResolveContext(value)
}

func (account *AccountBootstrapRecord) ResolveContext(value string) (*OrganizationRecord, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "personal" {
		value = account.PersonalOrgID
	}

	for i := range account.Organizations {
		organization := &account.Organizations[i]
		if organization.ID == value {
			return organization, nil
		}
		if organization.Handle != nil && value == "@"+*organization.Handle {
			return organization, nil
		}
	}
	return nil, fmt.Errorf("context %q is unavailable to this account", value)
}

func (organization *OrganizationRecord) ContextName() string {
	if organization.Handle != nil && *organization.Handle != "" {
		return "@" + *organization.Handle
	}
	return organization.ID
}
