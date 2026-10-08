package main

import (
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/telos-org/telos/internal/cloud"
)

type descriptionField struct {
	label  string
	value  string
	amount bool
	suffix string
}

func printDescriptionFields(out io.Writer, fields []descriptionField) {
	labelWidth, amountWidth := 9, 0
	for _, field := range fields {
		labelWidth = max(labelWidth, utf8.RuneCountInString(field.label))
		if field.amount {
			amountWidth = max(amountWidth, utf8.RuneCountInString(field.value))
		}
	}
	if amountWidth > 0 {
		labelWidth++
	}
	for _, field := range fields {
		if field.value == "" {
			fmt.Fprintln(out, field.label)
		} else if field.amount {
			fmt.Fprintf(out, "%-*s %*s%s\n", labelWidth, field.label, amountWidth, field.value, field.suffix)
		} else {
			fmt.Fprintf(out, "%-*s %s%s\n", labelWidth, field.label, field.value, field.suffix)
		}
	}
}

func cloudCostFields(session cloud.SessionRecord) []descriptionField {
	if session.Cost == nil {
		return []descriptionField{{label: "Cost", value: "Unavailable"}}
	}
	cost := session.Cost
	label := "  Spend"
	if cost.ExternalInference != nil {
		label = "  Telos spend"
	}
	fields := []descriptionField{
		{label: "Cost"},
		{label: label, value: formatCostAmount(cost.Telos.SpendMicroUSD), amount: cost.Telos.SpendMicroUSD != nil},
	}
	if categories := cost.Telos.Categories; categories != nil {
		for _, category := range []struct {
			label string
			value int64
		}{
			{"Inference", categories.InferenceMicroUSD},
			{"Compute", categories.ComputeMicroUSD},
			{"Storage", categories.StorageMicroUSD},
		} {
			if category.value > 0 {
				fields = append(fields, descriptionField{label: "    " + category.label, value: formatMicroUSD(category.value), amount: true})
			}
		}
		if cost.Telos.ReversedMicroUSD > 0 {
			fields = append(fields, descriptionField{label: "    Reversed", value: "−" + formatMicroUSD(cost.Telos.ReversedMicroUSD), amount: true})
		}
	} else if cost.Telos.SpendMicroUSD != nil && *cost.Telos.SpendMicroUSD > 0 {
		fields = append(fields, descriptionField{label: "    Breakdown", value: "Detailed breakdown unavailable"})
	}
	if accounting := cost.Telos.Accounting; accounting != nil && accounting.State != "current" && accounting.State != "" {
		status := accounting.State
		if accounting.AsOf != nil {
			status += " (as of " + *accounting.AsOf + ")"
		}
		fields = append(fields, descriptionField{label: "    Accounting", value: status})
	}
	if external := cost.ExternalInference; external != nil {
		field := descriptionField{
			label:  "  " + providerLabel(external.Provider),
			value:  formatCostAmount(external.EstimatedCostMicroUSD),
			amount: external.EstimatedCostMicroUSD != nil,
		}
		if external.EstimateKind == "api_equivalent" {
			field.suffix = " (API-equivalent)"
		}
		fields = append(fields, field)
	}
	return fields
}

func formatMicroUSD(amount int64) string {
	cents := amount / 10_000
	if amount%10_000 >= 5_000 {
		cents++
	}
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

func formatCostAmount(amount *int64) string {
	if amount == nil {
		return "Unavailable"
	}
	return formatMicroUSD(*amount)
}

func cloudSpendLabel(session cloud.SessionRecord) string {
	if session.Cost == nil {
		return "Unavailable"
	}
	value := formatCostAmount(session.Cost.Telos.SpendMicroUSD)
	if accounting := session.Cost.Telos.Accounting; session.Cost.Telos.SpendMicroUSD != nil && accounting != nil && accounting.State != "" && accounting.State != "current" {
		value += " (" + accounting.State + ")"
	}
	return value
}

func hasExternalInference(session cloud.SessionRecord) bool {
	return session.Inference != nil && (session.Inference.Source == "byok" || session.Inference.Source == "subscription")
}

func cloudProviderCostLabel(session cloud.SessionRecord) string {
	if !hasExternalInference(session) {
		if session.Inference == nil || session.Inference.Source != "managed" {
			return "Unavailable"
		}
		return "—"
	}
	var amount *int64
	if session.Cost != nil && session.Cost.ExternalInference != nil {
		amount = session.Cost.ExternalInference.EstimatedCostMicroUSD
	}
	value := formatCostAmount(amount) + " (" + providerLabel(session.Inference.Provider) + ")"
	if session.Inference.Source == "subscription" {
		value += " (API-equivalent)"
	}
	return value
}

func providerLabel(provider string) string {
	switch provider {
	case "anthropic":
		return "Anthropic"
	case "openai":
		return "OpenAI"
	case "openrouter":
		return "OpenRouter"
	case "xai":
		return "xAI"
	case "chatgpt-codex":
		return "Codex"
	case "xai-grok":
		return "Grok"
	case "":
		return "Provider"
	default:
		return provider
	}
}
