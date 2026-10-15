package cloud

// CreditsMicroUSD reads what is left of the context's usage credit from
// Cloud's billing ledger.
func (c *Client) CreditsMicroUSD() (int64, error) {
	usage, err := getJSONWithRetry[struct {
		BalanceMicroUSD int64 `json:"lifetime_balance_micro_usd"`
	}](c.context(), c, "/api/billing/usage")
	if err != nil {
		return 0, err
	}
	return usage.BalanceMicroUSD, nil
}
