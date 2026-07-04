package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
)

func cmdAnalyze(args []string) {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	env := fs.String("env", "", "Cloud environment")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos analyze SESSION... [--env ENV] [--json]")
		os.Exit(1)
	}
	if fs.NArg() > 1 {
		aggregate, err := getSessionSetAnalysis(fs.Args(), *env)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(aggregate)
			return
		}
		printSessionSetAnalysis(os.Stdout, aggregate)
		return
	}
	analysis, err := getSessionAnalysis(fs.Arg(0), *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(analysis)
		return
	}
	printSessionAnalysis(os.Stdout, analysis)
}

type sessionAnalysis = sessionapi.SessionAnalysis
type sessionSetAnalysis = sessionapi.SessionSetAnalysis
type sessionAnalysisDistribution = sessionapi.SessionAnalysisDistribution
type numericDistribution = sessionapi.NumericDistribution
type sessionSpecAnalysis = sessionapi.SessionSpecAnalysis
type sessionRetrySummary = sessionapi.SessionRetrySummary
type sessionErrorSummary = sessionapi.SessionErrorSummary
type sessionOutsideWorkspaceSummary = sessionapi.SessionOutsideWorkspaceSummary
type sessionArtifactInfo = sessionapi.SessionArtifactInfo

func getSessionSetAnalysis(sessionIDs []string, envID string) (sessionSetAnalysis, error) {
	analyses := make([]sessionAnalysis, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		analysis, err := getSessionAnalysis(sessionID, envID)
		if err != nil {
			return sessionSetAnalysis{}, err
		}
		analyses = append(analyses, analysis)
	}
	return analyzeSessionSet(analyses), nil
}

func getSessionAnalysis(sessionID, envID string) (sessionAnalysis, error) {
	if diagnostics, err := store().Diagnostics(sessionID); err == nil {
		return analyzeSessionDiagnostics(diagnostics), nil
	}
	if diagnostics, err := getCloudSessionDiagnostics(sessionID, envID); err == nil {
		return analyzeSessionDiagnostics(diagnostics), nil
	}
	if session, events, err := getSessionAnalysisInput(sessionID, envID); err == nil {
		return analyzeSessionEvents(session, events), nil
	} else {
		return sessionAnalysis{}, err
	}
}

func getCloudSessionDiagnostics(sessionID, envID string) (*sessionapi.SessionDiagnosticsResponse, error) {
	if ctx, ok := rootSessionContext(); ok {
		return cloud.NewClient(ctx.endpoint, ctx.token).GetDiagnostics(sessionID)
	}
	if envID == "" && !config.IsConfigured() {
		return nil, localSessionNotFoundError(sessionID)
	}
	clients, err := cloudSessionClients(envID)
	if err != nil && envID != "" {
		return nil, err
	}
	var cloudErr error = err
	for _, client := range clients {
		diagnostics, err := client.GetDiagnostics(sessionID)
		if err == nil {
			return diagnostics, nil
		}
		cloudErr = err
	}
	if cloudErr != nil {
		return nil, cloudErr
	}
	return nil, localSessionNotFoundError(sessionID)
}

func getSessionAnalysisInput(sessionID, envID string) (*sessionapi.Session, []sessionapi.SessionEvent, error) {
	if session, err := store().Get(sessionID); err == nil {
		events, err := store().Events(sessionID)
		if err != nil {
			return nil, nil, err
		}
		return session, events, nil
	}
	if ctx, ok := rootSessionContext(); ok {
		client := cloud.NewClient(ctx.endpoint, ctx.token)
		return cloudAnalysisInput(client, sessionID)
	}
	if envID != "" || config.IsConfigured() {
		clients, err := cloudSessionClients(envID)
		if err != nil && envID != "" {
			return nil, nil, err
		}
		var cloudErr error = err
		for _, client := range clients {
			session, events, err := cloudAnalysisInput(client, sessionID)
			if err == nil {
				return session, events, nil
			}
			cloudErr = err
		}
		if cloudErr != nil {
			return nil, nil, fmt.Errorf("session %s not found locally; cloud analysis lookup failed: %w", sessionID, cloudErr)
		}
	}
	return nil, nil, localSessionNotFoundError(sessionID)
}

func cloudAnalysisInput(client *cloud.Client, sessionID string) (*sessionapi.Session, []sessionapi.SessionEvent, error) {
	session, err := client.GetSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	events, err := client.GetEvents(sessionID)
	if err != nil {
		return nil, nil, err
	}
	return session, events, nil
}

func analyzeSessionDiagnostics(d *sessionapi.SessionDiagnosticsResponse) sessionAnalysis {
	return sessionapi.AnalyzeSessionDiagnostics(d)
}

func analyzeSessionSet(analyses []sessionAnalysis) sessionSetAnalysis {
	return sessionapi.AnalyzeSessionSet(analyses)
}

func analyzeSessionEvents(session *sessionapi.Session, events []sessionapi.SessionEvent) sessionAnalysis {
	return sessionapi.AnalyzeSessionEvents(session, events)
}

func printSessionSetAnalysis(out io.Writer, analysis sessionSetAnalysis) {
	fmt.Fprintln(out, "Benchmark Analysis")
	printDetailField(out, "sessions", fmt.Sprint(analysis.Count))
	printDetailField(out, "pass rate", fmt.Sprintf("%.1f%%", analysis.PassRate*100))
	printDetailField(out, "cost", formatCostValue(analysis.TotalCostUSD))
	if analysis.CostUnavailable {
		printDetailField(out, "cost unavailable", fmt.Sprintf("%d session%s", analysis.CostUnavailableN, plural(analysis.CostUnavailableN)))
	}
	printDetailField(out, "tokens", fmt.Sprintf("input %d, output %d", analysis.TotalInputTokens, analysis.TotalOutputTokens))
	printDetailField(out, "rounds", fmt.Sprint(analysis.TotalRounds))

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Results")
	printCountTable(out, analysis.Results)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Failure Taxonomy")
	printCountTable(out, analysis.Failures)
	if len(analysis.Budgets) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Budget Limits")
		printCountTable(out, analysis.Budgets)
	}
	if len(analysis.StopReasons) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Stop Reasons")
		printCountTable(out, analysis.StopReasons)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Distributions")
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "METRIC\tMIN\tP50\tP95\tMAX\tAVG")
	printDistributionRow(w, "cost", analysis.Distributions.CostUSD, formatCostValue)
	printDistributionRow(w, "input_tokens", analysis.Distributions.InputTokens, formatWholeNumber)
	printDistributionRow(w, "output_tokens", analysis.Distributions.OutputTokens, formatWholeNumber)
	printDistributionRow(w, "rounds", analysis.Distributions.Rounds, formatWholeNumber)
	_ = w.Flush()

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Sessions")
	w = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION\tSTATUS\tRESULT\tCOMPLETION\tROUNDS\tCOST\tTOKENS")
	for _, session := range analysis.Sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%d/%d\n", session.SessionID, orDash(session.Status), orDash(session.Result), orDash(session.Completion), session.Rounds, formatCost(analysisCost{USD: session.CostUSD, Unavailable: session.CostUnavailable}), session.InputTokens, session.OutputTokens)
	}
	_ = w.Flush()
}

func printSessionAnalysis(out io.Writer, analysis sessionAnalysis) {
	fmt.Fprintln(out, "Analysis")
	printDetailField(out, "session", analysis.SessionID)
	printDetailField(out, "status", analysis.Status)
	printDetailField(out, "result", analysis.Result)
	printDetailField(out, "completion", analysis.Completion)
	printDetailField(out, "rounds", fmt.Sprint(analysis.Rounds))
	printDetailField(out, "cost", formatCost(analysisCost{USD: analysis.CostUSD, Unavailable: analysis.CostUnavailable}))
	printDetailField(out, "tokens", fmt.Sprintf("input %d, output %d, cache-read %d, cache-write %d", analysis.InputTokens, analysis.OutputTokens, analysis.CacheReadTokens, analysis.CacheWriteTokens))

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Failure Taxonomy")
	printCountTable(out, analysis.Failures)
	if len(analysis.Budgets) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Budget Limits")
		printCountTable(out, analysis.Budgets)
	}
	if len(analysis.StopReasons) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Stop Reasons")
		printCountTable(out, analysis.StopReasons)
	}
	if len(analysis.SessionLogEvents) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Session Log Events")
		printCountTable(out, analysis.SessionLogEvents)
	}
	if len(analysis.Retries) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Retries")
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SPEC\tTURN\tSEQ\tATTEMPT\tDELAY_MS\tERROR\tHTTP")
		for _, retry := range analysis.Retries {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n", orDash(retry.SpecName), orDash(retry.TurnID), retry.Sequence, retry.Attempt, retry.DelayMS, orDash(retry.ErrorCode), statusOrDash(retry.ProviderStatusCode))
		}
		_ = w.Flush()
	}
	if len(analysis.Errors) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Errors")
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SPEC\tTURN\tSEQ\tERROR\tDETAIL")
		for _, errInfo := range analysis.Errors {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", orDash(errInfo.SpecName), orDash(errInfo.TurnID), errInfo.Sequence, orDash(errInfo.ErrorCode), orDash(errInfo.Error))
		}
		_ = w.Flush()
	}
	if len(analysis.OutsideWorkspace) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Outside Workspace Access")
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SPEC\tTURN\tACTION\tWRITE\tPATH")
		for _, access := range analysis.OutsideWorkspace {
			fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\n", orDash(access.SpecName), orDash(access.TurnID), orDash(access.Action), access.Write, orDash(access.Path))
		}
		_ = w.Flush()
	}
	if len(analysis.Artifacts) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Artifacts")
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SPEC\tEVIDENCE\tTRANSCRIPT\tLEDGER\tWORKSPACE")
		for _, artifact := range analysis.Artifacts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", orDash(artifact.SpecName), existsLabel(artifact.EvidenceExists), existsLabel(artifact.TranscriptExists), existsLabel(artifact.ObjectiveLedgerExists), existsLabel(artifact.WorkspaceExists))
		}
		_ = w.Flush()
	}
	if len(analysis.Specs) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Specs")
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SPEC\tRESULT\tCOMPLETION\tROUNDS\tCOST\tTOKENS")
		for _, spec := range analysis.Specs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d/%d\n", spec.Name, orDash(spec.Result), orDash(spec.Completion), spec.Rounds, formatCost(analysisCost{USD: spec.CostUSD, Unavailable: spec.CostUnavailable}), spec.InputTokens, spec.OutputTokens)
		}
		_ = w.Flush()
	}
}

func printCountTable(out io.Writer, counts map[string]int) {
	if len(counts) == 0 {
		fmt.Fprintln(out, "  none")
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		printDetailField(out, key, fmt.Sprint(counts[key]))
	}
}

func formatCostValue(value float64) string {
	if value == 0 {
		return "$0.00"
	}
	return fmt.Sprintf("$%.4f", value)
}

type analysisCost struct {
	USD         float64
	Unavailable bool
}

func formatCost(cost analysisCost) string {
	if cost.Unavailable {
		return formatCostValue(cost.USD) + " (unavailable)"
	}
	return formatCostValue(cost.USD)
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func printDistributionRow(out io.Writer, name string, dist numericDistribution, format func(float64) string) {
	fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\n", name, format(dist.Min), format(dist.P50), format(dist.P95), format(dist.Max), format(dist.Avg))
}

func formatWholeNumber(value float64) string {
	return fmt.Sprintf("%.0f", value)
}

func statusOrDash(status int) string {
	if status == 0 {
		return "-"
	}
	return fmt.Sprint(status)
}

func existsLabel(exists bool) string {
	if exists {
		return "yes"
	}
	return "no"
}
