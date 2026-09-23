package cloud

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type ChangeRequestActor struct {
	Subject string  `json:"subject"`
	Name    *string `json:"name"`
}

type ChangeRequestRecord struct {
	ID                  string              `json:"id"`
	DeploymentID        string              `json:"deployment_id"`
	Action              string              `json:"action"`
	Status              string              `json:"status"`
	RequireConfirmation bool                `json:"require_confirmation"`
	RequestedBy         ChangeRequestActor  `json:"requested_by"`
	ConfirmedBy         *ChangeRequestActor `json:"confirmed_by"`
	CreatedAt           string              `json:"created_at"`
	UpdatedAt           string              `json:"updated_at"`
	ConfirmedAt         *string             `json:"confirmed_at"`
	Message             string              `json:"message"`
	PackageRef          string              `json:"package_ref"`
	PackageDigest       string              `json:"package_digest"`
	AuthoredRevisionID  *string             `json:"authored_revision_id"`
	BaseRevisionID      *string             `json:"base_revision_id"`
	SourceRevisionID    *string             `json:"source_revision_id"`
	ResultRevisionID    *string             `json:"result_revision_id"`
	OperationID         *string             `json:"operation_id"`
	Error               *string             `json:"error"`
	QueuePosition       *int                `json:"queue_position"`
	CanConfirm          bool                `json:"can_confirm"`
	CanDiscard          bool                `json:"can_discard"`
	CanRetry            bool                `json:"can_retry"`
	Force               bool                `json:"force"`
	ReviewURL           string              `json:"review_url"`
}

func (r ChangeRequestRecord) Pending() bool {
	switch r.Status {
	case "queued", "awaiting_confirmation", "confirmed", "applying":
		return true
	default:
		return false
	}
}

type ChangePolicy struct {
	RequireConfirmation bool `json:"require_confirmation"`
	CanManage           bool `json:"can_manage"`
}

type SessionMutationResult struct {
	Outcome       string               `json:"outcome"`
	Deployment    *SessionRecord       `json:"deployment"`
	ChangeRequest *ChangeRequestRecord `json:"change_request,omitempty"`
}

// A missing capability endpoint identifies older Cloud versions. Other failures
// remain errors: inability to read capabilities must not silently disable a gate.
func (c *Client) DeploymentCapabilities() (*Capabilities, error) {
	resp, err := c.do("GET", "/api/capabilities", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &Capabilities{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var capabilities Capabilities
	if err := json.NewDecoder(resp.Body).Decode(&capabilities); err != nil {
		return nil, err
	}
	return &capabilities, nil
}

func (c *Client) GetChangePolicy(sessionID string) (*ChangePolicy, error) {
	resp, err := c.do("GET", "/api/deployments/"+url.PathEscape(sessionID)+"/change-policy", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var policy ChangePolicy
	if err := json.NewDecoder(resp.Body).Decode(&policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func (c *Client) ListChangeRequests(sessionID string) ([]ChangeRequestRecord, error) {
	resp, err := c.do("GET", "/api/deployments/"+url.PathEscape(sessionID)+"/change-requests", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var result struct {
		Requests []ChangeRequestRecord `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Requests, nil
}

func (c *Client) doSessionMutation(method, path string, body []byte, changeRequests bool) (*http.Response, error) {
	var headers map[string]string
	if changeRequests {
		headers = map[string]string{"X-Telos-Change-Requests": "1"}
	}
	return c.doRawWithHeaders(method, path, body, "application/json", headers)
}

func readSessionMutationResponse(resp *http.Response) (*SessionMutationResult, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var result SessionMutationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Outcome != "" {
		if result.Outcome != "requested" || result.Deployment == nil || result.Deployment.ID == "" ||
			result.ChangeRequest == nil || result.ChangeRequest.ID == "" ||
			result.ChangeRequest.DeploymentID != result.Deployment.ID || result.ChangeRequest.Status == "" {
			return nil, fmt.Errorf("invalid Cloud change request receipt")
		}
		return &result, nil
	}
	// An accepted response must identify the pending request. A bare deployment
	// would incorrectly imply that execution has already started.
	if resp.StatusCode == http.StatusAccepted {
		return nil, fmt.Errorf("Cloud accepted the change without a valid change request receipt")
	}
	var deployment SessionRecord
	if err := json.Unmarshal(raw, &deployment); err != nil {
		return nil, err
	}
	if deployment.ID == "" {
		return nil, fmt.Errorf("invalid Cloud deployment receipt")
	}
	return &SessionMutationResult{Deployment: &deployment}, nil
}
