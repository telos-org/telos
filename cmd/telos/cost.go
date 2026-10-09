package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/telos-org/telos/internal/cloud"
)

// costLookupTimeout bounds the runtime lookups telos list --wide and telos
// describe make for inference on your own API key or subscription.
var costLookupTimeout = 10 * time.Second

const costLookupWorkers = 4

// goalCost is what a Cloud Goal has cost so far. Cloud is compute and storage,
// billed by Telos. Inference is billed by Telos on Telos inference, by the
// provider on your own API key, or covered by your subscription. A nil amount
// could not be read.
type goalCost struct {
	Cloud     cloudCost     `json:"cloud"`
	Inference inferenceCost `json:"inference"`
}

type cloudCost struct {
	USD        *float64 `json:"usd"`
	ComputeUSD *float64 `json:"compute_usd"`
	StorageUSD *float64 `json:"storage_usd"`
}

type inferenceCost struct {
	USD      *float64 `json:"usd"`
	Source   string   `json:"source,omitempty"`
	Provider string   `json:"provider,omitempty"`
}

// goalCosts reads each Goal's cost from the billing Cloud returned with it,
// plus one runtime lookup per Goal on your own API key or subscription. A Goal
// without billing, such as one shared from another workspace by link, has no
// cost here.
func goalCosts(control *cloud.Client, goals []cloud.SessionRecord) []*goalCost {
	ctx, cancel := context.WithTimeout(context.Background(), costLookupTimeout)
	defer cancel()

	var wg sync.WaitGroup
	external := make([]*int64, len(goals))
	lookups := make(chan int)
	for range costLookupWorkers {
		wg.Go(func() {
			for index := range lookups {
				external[index], _ = control.InferenceCost(ctx, goals[index].ID)
			}
		})
	}
	for index, goal := range goals {
		if control != nil && needsInferenceLookup(goal) {
			lookups <- index
		}
	}
	close(lookups)
	wg.Wait()

	costs := make([]*goalCost, len(goals))
	for index, goal := range goals {
		billing := goal.Billing
		if billing == nil {
			continue
		}
		cost := &goalCost{}
		if goal.Inference != nil {
			cost.Inference.Source = goal.Inference.Source
			cost.Inference.Provider = goal.Inference.Provider
		}
		if billing.ComputeMicroUSD != nil && billing.StorageMicroUSD != nil {
			cost.Cloud = cloudCost{
				USD:        dollars(*billing.ComputeMicroUSD + *billing.StorageMicroUSD),
				ComputeUSD: dollars(*billing.ComputeMicroUSD),
				StorageUSD: dollars(*billing.StorageMicroUSD),
			}
		}
		if cost.Inference.Source == "managed" {
			cost.Inference.USD = dollars(billing.InferenceMicroUSD)
		}
		if external[index] != nil {
			cost.Inference.USD = dollars(*external[index])
		}
		costs[index] = cost
	}
	return costs
}

// needsInferenceLookup reports whether a Goal's inference cost comes from its
// runtime: inference on your own API key or subscription, in your workspace.
func needsInferenceLookup(goal cloud.SessionRecord) bool {
	return goal.Billing != nil && goal.Inference != nil && goal.Inference.Source != "managed"
}

func dollars(microUSD int64) *float64 {
	usd := float64(microUSD) / 1_000_000
	return &usd
}

func formatUSD(usd float64) string {
	return fmt.Sprintf("$%.2f", usd)
}

func printGoalCost(out io.Writer, cost *goalCost) {
	if cost == nil {
		return
	}
	printSummaryField(out, "Cloud", cloudCostText(cost.Cloud))
	printSummaryField(out, "Inference", inferenceCostText(cost.Inference))
}

// cloudCostText reads "$1.25 (compute $1.00, storage $0.25)".
func cloudCostText(cost cloudCost) string {
	if cost.USD == nil {
		return "unavailable"
	}
	var parts []string
	for _, part := range []struct {
		name string
		usd  *float64
	}{{"compute", cost.ComputeUSD}, {"storage", cost.StorageUSD}} {
		if part.usd != nil && formatUSD(*part.usd) != formatUSD(0) {
			parts = append(parts, part.name+" "+formatUSD(*part.usd))
		}
	}
	if len(parts) == 0 {
		return formatUSD(*cost.USD)
	}
	return fmt.Sprintf("%s (%s)", formatUSD(*cost.USD), strings.Join(parts, ", "))
}

// inferenceCostText reads "$3.40 (Anthropic API key)", naming who bills it.
func inferenceCostText(cost inferenceCost) string {
	amount := "unavailable"
	if cost.USD != nil {
		amount = formatUSD(*cost.USD)
	}
	if payer := inferencePayer(cost); payer != "" {
		return amount + " (" + payer + ")"
	}
	return amount
}

func inferencePayer(cost inferenceCost) string {
	switch cost.Source {
	case "managed":
		return "Telos"
	case "byok":
		return strings.TrimSpace(providerName(cost.Provider) + " API key")
	case "subscription":
		return strings.TrimSpace(providerName(cost.Provider) + " subscription")
	default:
		return ""
	}
}

var providerNames = map[string]string{
	"anthropic":     "Anthropic",
	"openai":        "OpenAI",
	"openrouter":    "OpenRouter",
	"xai":           "xAI",
	"chatgpt-codex": "ChatGPT",
	"xai-grok":      "Grok",
}

func providerName(provider string) string {
	if name, ok := providerNames[provider]; ok {
		return name
	}
	return provider
}

// costCell is a list --wide amount: ? when it could not be read.
func costCell(usd *float64) string {
	if usd == nil {
		return "?"
	}
	return formatUSD(*usd)
}
