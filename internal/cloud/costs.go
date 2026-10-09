package cloud

import (
	"context"
	"net/url"
)

// GoalBilling is what Telos has billed a Goal so far, by category, net of
// reversals. Cloud returns it to members of the Goal's workspace.
type GoalBilling struct {
	InferenceMicroUSD int64  `json:"inference_micro_usd"`
	ComputeMicroUSD   *int64 `json:"compute_micro_usd"`
	StorageMicroUSD   *int64 `json:"storage_micro_usd"`
}

// ListSessionsWithBilling lists the context's Goals with each one's billing.
func (c *Client) ListSessionsWithBilling() ([]SessionRecord, error) {
	return c.listSessions("/api/deployments?include_billing=true")
}

// GetSessionWithBilling returns a Goal with its billing, which Cloud gives
// workspace members who aren't operators only when asked.
func (c *Client) GetSessionWithBilling(sessionID string) (*SessionRecord, error) {
	return getJSONWithRetry[SessionRecord](context.Background(), c, "/api/deployments/"+url.PathEscape(sessionID)+"?include_billing=true")
}

// InferenceCost returns the inference cost a Goal's runtime has recorded on
// your own API key or subscription, or nil when it has none to report.
func (c *Client) InferenceCost(ctx context.Context, deploymentID string) (*int64, error) {
	response, err := getJSONWithRetry[struct {
		MicroUSD *int64 `json:"estimated_cost_micro_usd"`
	}](ctx, c, "/api/deployments/"+url.PathEscape(deploymentID)+"/inference-usage")
	if err != nil {
		return nil, err
	}
	return response.MicroUSD, nil
}
