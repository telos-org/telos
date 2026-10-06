package cloud

import (
	"context"
	"net/url"
	"sync"
	"time"
)

type SessionCost struct {
	Telos             TelosCost              `json:"telos"`
	ExternalInference *ExternalInferenceCost `json:"external_inference"`
}

type TelosCost struct {
	SpendMicroUSD    *int64            `json:"spend_micro_usd"`
	Categories       *CostCategories   `json:"categories"`
	ReversedMicroUSD int64             `json:"reversed_micro_usd,omitempty"`
	Accounting       *AccountingStatus `json:"accounting"`
}

type CostCategories struct {
	InferenceMicroUSD int64 `json:"inference_micro_usd,omitempty"`
	ComputeMicroUSD   int64 `json:"compute_micro_usd,omitempty"`
	StorageMicroUSD   int64 `json:"storage_micro_usd,omitempty"`
}

type AccountingStatus struct {
	State   string                   `json:"state"`
	AsOf    *string                  `json:"as_of"`
	Sources []AccountingSourceStatus `json:"sources"`
}

type AccountingSourceStatus struct {
	Source string  `json:"source"`
	State  string  `json:"state"`
	AsOf   *string `json:"as_of"`
}

type ExternalInferenceCost struct {
	Provider              string `json:"provider"`
	EstimateKind          string `json:"estimate_kind"`
	EstimatedCostMicroUSD *int64 `json:"estimated_cost_micro_usd"`
}

type deploymentSpend struct {
	DeploymentID            string            `json:"deployment_id"`
	GrossMicroUSD           int64             `json:"gross_spend_micro_usd"`
	ReversalsMicroUSD       int64             `json:"reversals_micro_usd"`
	NetMicroUSD             *int64            `json:"net_spend_micro_usd"`
	NonInferenceNetMicroUSD *int64            `json:"non_inference_net_spend_micro_usd"`
	InferenceMicroUSD       int64             `json:"inference_micro_usd"`
	ComputeMicroUSD         int64             `json:"compute_micro_usd"`
	StorageMicroUSD         int64             `json:"storage_micro_usd"`
	Accounting              *AccountingStatus `json:"accounting"`
}

// PopulateSessionCosts enriches only the supplied (already limited) sessions.
// Accounting is optional: a failed cost lookup must not hide a readable goal.
func (c *Client) PopulateSessionCosts(sessions []SessionRecord) {
	if len(sessions) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var spendRows []deploymentSpend
	var work sync.WaitGroup
	work.Go(func() {
		response, err := getJSONWithRetry[struct {
			Deployments []deploymentSpend `json:"deployments"`
		}](ctx, c, "/api/billing/deployments")
		if err == nil {
			spendRows = response.Deployments
		}
	})
	jobs := make(chan int, len(sessions))
	for index := range sessions {
		jobs <- index
	}
	close(jobs)
	for range min(4, len(sessions)) {
		work.Go(func() {
			for index := range jobs {
				session := &sessions[index]
				// Older Cloud lists omit inference; keep rollout compatible without
				// inferring billing source from a model name or workspace defaults.
				if session.Inference == nil {
					detail, err := getJSONWithRetry[SessionRecord](ctx, c, "/api/deployments/"+url.PathEscape(session.ID))
					if err == nil {
						session.Inference = detail.Inference
					}
				}
				if session.Inference == nil {
					continue
				}
				switch session.Inference.Source {
				case "managed":
					session.Cost = &SessionCost{}
				case "byok", "subscription":
					external := &ExternalInferenceCost{
						Provider:     session.Inference.Provider,
						EstimateKind: "api_cost",
					}
					if session.Inference.Source == "subscription" {
						external.EstimateKind = "api_equivalent"
					}
					session.Cost = &SessionCost{ExternalInference: external}
					usage, err := getJSONWithRetry[struct {
						EstimatedCostMicroUSD *int64 `json:"estimated_cost_micro_usd"`
					}](ctx, c, "/api/deployments/"+url.PathEscape(session.ID)+"/inference-usage")
					if err == nil && usage.EstimatedCostMicroUSD != nil && *usage.EstimatedCostMicroUSD >= 0 {
						external.EstimatedCostMicroUSD = usage.EstimatedCostMicroUSD
					}
				}
			}
		})
	}
	work.Wait()
	spendByID := make(map[string]deploymentSpend, len(spendRows))
	for _, row := range spendRows {
		spendByID[row.DeploymentID] = row
	}
	for index := range sessions {
		session := &sessions[index]
		if row, ok := spendByID[session.ID]; ok && session.Cost != nil {
			session.Cost.Telos = row.cost(session.Cost.ExternalInference != nil)
		}
	}
}

// Keep these reconciliation rules in step with the web's HeaderSpend breakdown.
func (row deploymentSpend) cost(external bool) TelosCost {
	amount := row.NetMicroUSD
	if external && row.NonInferenceNetMicroUSD != nil {
		amount = row.NonInferenceNetMicroUSD
	}
	cost := TelosCost{SpendMicroUSD: amount, Accounting: row.Accounting}
	if amount == nil || *amount < 0 {
		cost.SpendMicroUSD = nil
		return cost
	}
	categories := CostCategories{
		ComputeMicroUSD: row.ComputeMicroUSD,
		StorageMicroUSD: row.StorageMicroUSD,
	}
	if !external {
		categories.InferenceMicroUSD = row.InferenceMicroUSD
	}
	if categories.InferenceMicroUSD < 0 || categories.ComputeMicroUSD < 0 || categories.StorageMicroUSD < 0 {
		return cost
	}
	gross := categories.InferenceMicroUSD + categories.ComputeMicroUSD + categories.StorageMicroUSD
	reversed := max(row.ReversalsMicroUSD, 0)
	reconciles := gross == row.GrossMicroUSD && max(gross-reversed, 0) == *amount
	if external {
		reversed = max(gross-*amount, 0)
		reconciles = row.NonInferenceNetMicroUSD != nil
	}
	if gross > 0 && *amount <= gross && reconciles {
		cost.Categories = &categories
		cost.ReversedMicroUSD = reversed
	}
	return cost
}
