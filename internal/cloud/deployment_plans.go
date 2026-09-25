package cloud

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type DeploymentPlanSkill struct {
	Name    string `json:"name"`
	Ref     string `json:"ref"`
	Digest  string `json:"digest"`
	Starred bool   `json:"starred"`
}

type DeploymentPlanPreview struct {
	BaseSpec       string                `json:"base_spec"`
	ProposedSpec   string                `json:"proposed_spec"`
	BaseSkills     []DeploymentPlanSkill `json:"base_skills"`
	ProposedSkills []DeploymentPlanSkill `json:"proposed_skills"`
}

type DeploymentPlanCreation struct {
	Name          string              `json:"name"`
	AgentModel    *string             `json:"agent_model"`
	AgentThinking *string             `json:"agent_thinking,omitempty"`
	Inference     *InferenceSelection `json:"inference"`
	SecretIDs     []string            `json:"secret_ids"`
}

type DeploymentPlanAccess struct {
	CanPlan  bool `json:"can_plan"`
	CanApply bool `json:"can_apply"`
}

type DeploymentPlanOptions struct {
	Mode         string                `json:"mode"`
	DeploymentID string                `json:"deployment_id,omitempty"`
	Create       *SessionCreateOptions `json:"create,omitempty"`
	Update       *SessionUpdateOptions `json:"update,omitempty"`
	AutoConfirm  bool                  `json:"auto_confirm,omitempty"`
}

// PlanArtifactClient stages private, content-addressed Registry artifacts. The
// original client and ordinary `telos push` retain their publishing behavior.
func (c *Client) PlanArtifactClient() *Client {
	copy := *c
	copy.planArtifacts = true
	return &copy
}

func (c *Client) DeploymentPlanAccess(deploymentID string) (*DeploymentPlanAccess, error) {
	path := "/api/deployment-plans/access"
	if deploymentID != "" {
		path += "?" + url.Values{"deployment_id": {deploymentID}}.Encode()
	}
	resp, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var access DeploymentPlanAccess
	if err := json.NewDecoder(resp.Body).Decode(&access); err != nil {
		return nil, err
	}
	return &access, nil
}

func (c *Client) CreateDeploymentPlan(options DeploymentPlanOptions) (*ChangeRequestRecord, error) {
	body, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	return c.deploymentPlanRequest(http.MethodPost, "/api/deployment-plans", body)
}

func changeRequestPath(deploymentID, requestID string) string {
	return "/api/deployments/" + url.PathEscape(deploymentID) + "/change-requests/" + url.PathEscape(requestID)
}

func (c *Client) GetChangeRequest(deploymentID, requestID string) (*ChangeRequestRecord, error) {
	return c.deploymentPlanRequest(http.MethodGet, changeRequestPath(deploymentID, requestID), nil)
}

func (c *Client) ConfirmChangeRequest(request ChangeRequestRecord) (*ChangeRequestRecord, error) {
	body, err := json.Marshal(struct {
		ExpectedCurrentRevisionID *string `json:"expected_current_revision_id"`
	}{request.BaseRevisionID})
	if err != nil {
		return nil, err
	}
	return c.deploymentPlanRequest(http.MethodPost, changeRequestPath(request.DeploymentID, request.ID)+"/confirm", body)
}

func (c *Client) DiscardChangeRequest(deploymentID, requestID string) (*ChangeRequestRecord, error) {
	return c.deploymentPlanRequest(http.MethodPost, changeRequestPath(deploymentID, requestID)+"/discard", []byte(`{}`))
}

func (c *Client) deploymentPlanRequest(method, path string, body []byte) (*ChangeRequestRecord, error) {
	resp, err := c.do(method, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return nil, readError(resp)
	}
	var request ChangeRequestRecord
	if err := json.NewDecoder(resp.Body).Decode(&request); err != nil {
		return nil, err
	}
	if request.ID == "" || request.DeploymentID == "" || request.Status == "" {
		return nil, fmt.Errorf("invalid Cloud deployment plan receipt")
	}
	return &request, nil
}
