package cloud

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type ChangeRequestActor struct {
	Subject string  `json:"subject"`
	Name    *string `json:"name"`
}

type ChangeRequestRecord struct {
	UpdateNumber            int                     `json:"update_number,omitempty"`
	PreparedPlanID          string                  `json:"prepared_plan_id,omitempty"`
	CurrentRevisionID       *string                 `json:"current_revision_id,omitempty"`
	LegacyConfirmationValid *bool                   `json:"legacy_confirmation_valid,omitempty"`
	CanEdit                 bool                    `json:"can_edit,omitempty"`
	PlanStale               bool                    `json:"plan_stale,omitempty"`
	Kind                    string                  `json:"kind,omitempty"`
	ErrorCode               *string                 `json:"error_code,omitempty"`
	ID                      string                  `json:"id"`
	Mode                    string                  `json:"mode"`
	AutoConfirm             bool                    `json:"auto_confirm"`
	Preview                 *DeploymentPlanPreview  `json:"preview"`
	Creation                *DeploymentPlanCreation `json:"creation"`
	DeploymentID            string                  `json:"deployment_id"`
	Action                  string                  `json:"action"`
	Status                  string                  `json:"status"`
	RequestedBy             ChangeRequestActor      `json:"requested_by"`
	ConfirmedBy             *ChangeRequestActor     `json:"confirmed_by"`
	CreatedAt               string                  `json:"created_at"`
	UpdatedAt               string                  `json:"updated_at"`
	ConfirmedAt             *string                 `json:"confirmed_at"`
	Message                 string                  `json:"message"`
	PackageRef              string                  `json:"package_ref"`
	PackageDigest           string                  `json:"package_digest"`
	AuthoredRevisionID      *string                 `json:"authored_revision_id"`
	BaseRevisionID          *string                 `json:"base_revision_id"`
	SourceRevisionID        *string                 `json:"source_revision_id"`
	SourceSnapshotID        *string                 `json:"source_snapshot_id"`
	ResultRevisionID        *string                 `json:"result_revision_id"`
	OperationID             *string                 `json:"operation_id"`
	Error                   *string                 `json:"error"`
	QueuePosition           *int                    `json:"queue_position"`
	CanConfirm              bool                    `json:"can_confirm"`
	CanDiscard              bool                    `json:"can_discard"`
	CanRetry                bool                    `json:"can_retry"`
	BlockedReason           *string                 `json:"blocked_reason,omitempty"`
	IsProvisioning          bool                    `json:"is_provisioning,omitempty"`
	Force                   bool                    `json:"force"`
	ReviewURL               string                  `json:"review_url"`
}

// The ID namespace selects the API route; Kind is validated against it when
// decoding a proposal, so routing, confirmation and display cannot disagree.
func (r ChangeRequestRecord) IsPlan() bool {
	return strings.HasPrefix(r.ID, "plan_")
}

func (r ChangeRequestRecord) Started() bool {
	return r.Status == "confirmed" || r.Status == "applying" || r.Status == "applied"
}

func (r ChangeRequestRecord) Pending() bool {
	if r.Mode == "preview" {
		return false
	}
	switch r.Status {
	case "queued", "awaiting_confirmation", "confirmed", "applying":
		return true
	default:
		return false
	}
}

// A missing capability endpoint identifies older Cloud versions. Other failures
// remain errors: inability to read capabilities must not silently disable a gate.
func (c *Client) DeploymentCapabilities() (*Capabilities, error) {
	capabilities, err := c.RegistryCapabilities()
	if IsStatus(err, http.StatusNotFound) {
		return &Capabilities{}, nil
	}
	return capabilities, err
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
