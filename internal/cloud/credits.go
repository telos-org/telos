package cloud

import "context"

// Credits is what is left of a workspace's usage credit, and whether its Goals
// can do agent work.
type Credits struct {
	BalanceMicroUSD int64
	WorkAllowed     bool
	// WorkBlockReason says why agent work is paused, like credits_exhausted.
	WorkBlockReason string
}

// Credits reads the context's credit balance from Cloud's billing ledger.
func (c *Client) Credits() (*Credits, error) {
	usage, err := getJSONWithRetry[struct {
		BalanceMicroUSD int64   `json:"lifetime_balance_micro_usd"`
		WorkAllowed     bool    `json:"work_allowed"`
		WorkBlockReason *string `json:"work_block_reason"`
	}](context.Background(), c, "/api/billing/usage")
	if err != nil {
		return nil, err
	}
	credits := &Credits{BalanceMicroUSD: usage.BalanceMicroUSD, WorkAllowed: usage.WorkAllowed}
	if usage.WorkBlockReason != nil {
		credits.WorkBlockReason = *usage.WorkBlockReason
	}
	return credits, nil
}
