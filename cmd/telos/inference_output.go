package main

import (
	"io"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

func printCloudInferenceSummary(out io.Writer, session cloud.SessionRecord) {
	model := session.AgentModel
	if summary := session.Inference; summary != nil {
		printSummaryField(out, "Inference", inferenceSourceLabel(summary.Source))
		if summary.ConnectionName != "" {
			printSummaryField(out, "Connection", summary.ConnectionName)
		}
		if summary.Model != "" {
			model = summary.Model
		}
		if summary.Source == "managed" {
			if model == "" && summary.Tier != "" {
				model = "telos/" + summary.Tier
			}
			if model == "telos-bifrost/telos/default" || model == "telos-bifrost/telos/max" {
				model = strings.TrimPrefix(model, "telos-bifrost/")
			}
		}
	}
	if model != "" {
		printSummaryField(out, "Model", model)
	}
	if session.AgentThinking != "" {
		printSummaryField(out, "Thinking", session.AgentThinking+" (requested)")
	}
}
