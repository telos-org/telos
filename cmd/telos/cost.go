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
// provider on your own API key, or covered by your subscription. --json has
// exact dollars, describe and list round to cents, and a nil amount could not
// be read.
type goalCost struct {
	Cloud     cloudCost     `json:"cloud"`
	Inference inferenceCost `json:"inference"`
}

type cloudCost struct {
	USD        *float64 `json:"usd"`
	ComputeUSD *float64 `json:"compute_usd"`
	StorageUSD *float64 `json:"storage_usd"`
	// compute and storage are in micro-USD.
	compute, storage *int64
}

type inferenceCost struct {
	USD      *float64 `json:"usd"`
	Source   string   `json:"source,omitempty"`
	Provider string   `json:"provider,omitempty"`
	// microUSD is the amount in micro-USD.
	microUSD *int64
}

func newCloudCost(computeMicroUSD, storageMicroUSD int64) cloudCost {
	return cloudCost{
		USD:        dollars(computeMicroUSD + storageMicroUSD),
		ComputeUSD: dollars(computeMicroUSD),
		StorageUSD: dollars(storageMicroUSD),
		compute:    &computeMicroUSD,
		storage:    &storageMicroUSD,
	}
}

func newInferenceCost(source, provider string, microUSD *int64) inferenceCost {
	cost := inferenceCost{Source: source, Provider: provider, microUSD: microUSD}
	if microUSD != nil {
		cost.USD = dollars(*microUSD)
	}
	return cost
}

// goalCosts reads each Goal's cost from the billing Cloud returned with it,
// plus one runtime lookup per Goal on your own API key or subscription. A Goal
// shared from another workspace by link has no cost here, nor does a Goal
// Cloud returned without billing unless the list says your workspace owns it.
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
		if control != nil && hasCost(goal) && billedByProvider(goal) {
			lookups <- index
		}
	}
	close(lookups)
	wg.Wait()

	costs := make([]*goalCost, len(goals))
	for index, goal := range goals {
		if !hasCost(goal) {
			continue
		}
		cost := &goalCost{}
		var inference *int64
		if billing := goal.Billing; billing != nil {
			if billing.ComputeMicroUSD != nil && billing.StorageMicroUSD != nil {
				cost.Cloud = newCloudCost(*billing.ComputeMicroUSD, *billing.StorageMicroUSD)
			}
			inference = &billing.InferenceMicroUSD
		}
		if billedByProvider(goal) {
			inference = external[index]
		}
		source, provider := "", ""
		if goal.Inference != nil {
			source, provider = goal.Inference.Source, goal.Inference.Provider
		}
		cost.Inference = newInferenceCost(source, provider, inference)
		costs[index] = cost
	}
	return costs
}

func hasCost(goal cloud.SessionRecord) bool {
	return goal.Billing != nil || goal.AccessSource == "owned"
}

// billedByProvider reports whether a Goal's inference runs on your own API key
// or subscription, whose cost its runtime records.
func billedByProvider(goal cloud.SessionRecord) bool {
	return goal.Inference != nil && (goal.Inference.Source == "byok" || goal.Inference.Source == "subscription")
}

func dollars(microUSD int64) *float64 {
	usd := float64(microUSD) / 1_000_000
	return &usd
}

// cents rounds micro-USD half up to whole cents.
func cents(microUSD int64) int64 {
	return (microUSD + 5_000) / 10_000
}

func formatCents(cents int64) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// formatMicroUSD shows an amount in cents; a charge under half a cent shows as
// <$0.01 rather than nothing.
func formatMicroUSD(microUSD int64) string {
	if microUSD > 0 && cents(microUSD) == 0 {
		return "<$0.01"
	}
	return formatCents(cents(microUSD))
}

func printGoalCost(out io.Writer, cost *goalCost) {
	if cost == nil {
		return
	}
	printSummaryField(out, "Cloud", cloudCostText(cost.Cloud))
	printSummaryField(out, "Inference", inferenceCostText(cost.Inference))
}

// cloudCostTotal is the Cloud amount as describe and list show it: the sum of
// the rounded parts, so the breakdown adds up. It is empty when unreadable.
func cloudCostTotal(cost cloudCost) string {
	if cost.compute == nil || cost.storage == nil {
		return ""
	}
	if total := cents(*cost.compute) + cents(*cost.storage); total > 0 {
		return formatCents(total)
	}
	return formatMicroUSD(*cost.compute + *cost.storage)
}

// cloudCostText reads "$1.25 (compute $1.00, storage $0.25)".
func cloudCostText(cost cloudCost) string {
	total := cloudCostTotal(cost)
	if total == "" {
		return "unavailable"
	}
	var parts []string
	if compute := cents(*cost.compute); compute > 0 {
		parts = append(parts, "compute "+formatCents(compute))
	}
	if storage := cents(*cost.storage); storage > 0 {
		parts = append(parts, "storage "+formatCents(storage))
	}
	if len(parts) == 0 {
		return total
	}
	return total + " (" + strings.Join(parts, ", ") + ")"
}

// inferenceCostText reads "$3.40 (Anthropic API key)", naming who bills it.
func inferenceCostText(cost inferenceCost) string {
	amount := "unavailable"
	if cost.microUSD != nil {
		amount = formatMicroUSD(*cost.microUSD)
	}
	return amount + " (" + inferencePayer(cost) + ")"
}

func inferencePayer(cost inferenceCost) string {
	switch cost.Source {
	case "byok":
		return strings.TrimSpace(providerName(cost.Provider) + " API key")
	case "subscription":
		return strings.TrimSpace(providerName(cost.Provider) + " subscription")
	default:
		// Anything else is in Telos billing.
		return "Telos"
	}
}

var providerNames = map[string]string{
	"anthropic":     "Anthropic",
	"openai":        "OpenAI",
	"openrouter":    "OpenRouter",
	"xai":           "xAI",
	"chatgpt-codex": "ChatGPT",
	"xai-grok":      "xAI",
}

func providerName(provider string) string {
	if name, ok := providerNames[provider]; ok {
		return name
	}
	return provider
}

// costCells are a Goal's list --wide amounts: - when it has no cost here, and
// ? when an amount could not be read.
func costCells(cost *goalCost) (string, string) {
	if cost == nil {
		return "-", "-"
	}
	cloudAmount := cloudCostTotal(cost.Cloud)
	if cloudAmount == "" {
		cloudAmount = "?"
	}
	inference := "?"
	if cost.Inference.microUSD != nil {
		inference = formatMicroUSD(*cost.Inference.microUSD)
	}
	return cloudAmount, inference
}
