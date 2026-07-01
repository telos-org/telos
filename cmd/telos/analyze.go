package main

import (
	"flag"
	"fmt"
	"io"
	"math"
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

type sessionAnalysis struct {
	SessionID        string                           `json:"session_id"`
	Status           string                           `json:"status"`
	Result           string                           `json:"result,omitempty"`
	Completion       string                           `json:"completion,omitempty"`
	CostUSD          float64                          `json:"cost_usd,omitempty"`
	CostUnavailable  bool                             `json:"cost_unavailable,omitempty"`
	InputTokens      int                              `json:"input_tokens,omitempty"`
	OutputTokens     int                              `json:"output_tokens,omitempty"`
	CacheReadTokens  int                              `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int                              `json:"cache_write_tokens,omitempty"`
	Rounds           int                              `json:"rounds,omitempty"`
	Failures         map[string]int                   `json:"failures"`
	Budgets          map[string]int                   `json:"budgets,omitempty"`
	StopReasons      map[string]int                   `json:"stop_reasons,omitempty"`
	SessionLogEvents map[string]int                   `json:"session_log_events,omitempty"`
	Retries          []sessionRetrySummary            `json:"retries,omitempty"`
	Errors           []sessionErrorSummary            `json:"errors,omitempty"`
	OutsideWorkspace []sessionOutsideWorkspaceSummary `json:"outside_workspace_access,omitempty"`
	Artifacts        []sessionArtifactInfo            `json:"artifacts,omitempty"`
	Specs            []sessionSpecAnalysis            `json:"specs,omitempty"`
}

type sessionSetAnalysis struct {
	Count             int                         `json:"count"`
	Sessions          []sessionAnalysis           `json:"sessions"`
	Results           map[string]int              `json:"results"`
	PassRate          float64                     `json:"pass_rate,omitempty"`
	TotalCostUSD      float64                     `json:"total_cost_usd,omitempty"`
	CostUnavailable   bool                        `json:"cost_unavailable,omitempty"`
	CostUnavailableN  int                         `json:"cost_unavailable_sessions,omitempty"`
	TotalInputTokens  int                         `json:"total_input_tokens,omitempty"`
	TotalOutputTokens int                         `json:"total_output_tokens,omitempty"`
	TotalRounds       int                         `json:"total_rounds,omitempty"`
	Failures          map[string]int              `json:"failures"`
	Budgets           map[string]int              `json:"budgets,omitempty"`
	StopReasons       map[string]int              `json:"stop_reasons,omitempty"`
	Distributions     sessionAnalysisDistribution `json:"distributions"`
}

type sessionAnalysisDistribution struct {
	CostUSD      numericDistribution `json:"cost_usd"`
	InputTokens  numericDistribution `json:"input_tokens"`
	OutputTokens numericDistribution `json:"output_tokens"`
	Rounds       numericDistribution `json:"rounds"`
}

type numericDistribution struct {
	Count int     `json:"count"`
	Min   float64 `json:"min"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	Max   float64 `json:"max"`
	Avg   float64 `json:"avg"`
}

type sessionSpecAnalysis struct {
	Name            string         `json:"name"`
	Result          string         `json:"result,omitempty"`
	Completion      string         `json:"completion,omitempty"`
	CostUSD         float64        `json:"cost_usd,omitempty"`
	CostUnavailable bool           `json:"cost_unavailable,omitempty"`
	InputTokens     int            `json:"input_tokens,omitempty"`
	OutputTokens    int            `json:"output_tokens,omitempty"`
	Rounds          int            `json:"rounds,omitempty"`
	Failures        map[string]int `json:"failures,omitempty"`
}

type sessionRetrySummary struct {
	SpecName           string `json:"spec_name,omitempty"`
	TurnID             string `json:"turn_id,omitempty"`
	Sequence           int    `json:"sequence,omitempty"`
	Attempt            int    `json:"attempt,omitempty"`
	DelayMS            int    `json:"delay_ms,omitempty"`
	ErrorCode          string `json:"error_code,omitempty"`
	ProviderStatusCode int    `json:"provider_status_code,omitempty"`
}

type sessionErrorSummary struct {
	SpecName  string `json:"spec_name,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Sequence  int    `json:"sequence,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

type sessionOutsideWorkspaceSummary struct {
	SpecName string `json:"spec_name,omitempty"`
	TurnID   string `json:"turn_id,omitempty"`
	Action   string `json:"action,omitempty"`
	Path     string `json:"path,omitempty"`
	Write    bool   `json:"write,omitempty"`
}

type sessionArtifactInfo struct {
	SpecName              string `json:"spec_name,omitempty"`
	EvidencePath          string `json:"evidence_path,omitempty"`
	EvidenceExists        bool   `json:"evidence_exists"`
	TranscriptPath        string `json:"transcript_path,omitempty"`
	TranscriptExists      bool   `json:"transcript_exists"`
	ObjectiveLedgerPath   string `json:"objective_ledger_path,omitempty"`
	ObjectiveLedgerExists bool   `json:"objective_ledger_exists"`
	WorkspacePath         string `json:"workspace_path,omitempty"`
	WorkspaceExists       bool   `json:"workspace_exists"`
}

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
	if d == nil {
		return sessionAnalysis{Failures: map[string]int{}}
	}
	out := sessionAnalysis{
		SessionID:        d.SessionID,
		Status:           string(d.Status),
		Result:           ptrString(d.Result),
		Completion:       ptrString(d.CompletionReason),
		CostUSD:          d.Totals.CostUSD,
		CostUnavailable:  d.Totals.CostUnavailable,
		InputTokens:      d.Totals.InputTokens,
		OutputTokens:     d.Totals.OutputTokens,
		CacheReadTokens:  d.Totals.CacheReadTokens,
		CacheWriteTokens: d.Totals.CacheWriteTokens,
		Rounds:           d.Totals.Rounds,
		Failures:         cloneCounts(d.Failures),
		Budgets:          cloneCounts(d.BudgetExceeded),
		StopReasons:      cloneCounts(d.StopReasons),
		SessionLogEvents: cloneCounts(d.SessionLogEvents),
	}
	if len(out.Budgets) == 0 {
		out.Budgets = nil
	}
	if len(out.StopReasons) == 0 {
		out.StopReasons = nil
	}
	if len(out.SessionLogEvents) == 0 {
		out.SessionLogEvents = nil
	}
	for _, retry := range d.Retries {
		out.Retries = append(out.Retries, sessionRetrySummary{
			SpecName:           retry.SpecName,
			TurnID:             retry.TurnID,
			Sequence:           retry.Sequence,
			Attempt:            retry.Attempt,
			DelayMS:            retry.DelayMS,
			ErrorCode:          retry.ErrorCode,
			ProviderStatusCode: retry.ProviderStatusCode,
		})
	}
	for _, errInfo := range d.Errors {
		out.Errors = append(out.Errors, sessionErrorSummary{
			SpecName:  errInfo.SpecName,
			TurnID:    errInfo.TurnID,
			Sequence:  errInfo.Sequence,
			ErrorCode: errInfo.ErrorCode,
			Error:     errInfo.Error,
		})
	}
	for _, access := range d.OutsideWorkspace {
		out.OutsideWorkspace = append(out.OutsideWorkspace, sessionOutsideWorkspaceSummary{
			SpecName: access.SpecName,
			TurnID:   access.TurnID,
			Action:   access.Action,
			Path:     access.Path,
			Write:    access.Write,
		})
	}
	for _, artifact := range d.Artifacts {
		out.Artifacts = append(out.Artifacts, sessionArtifactInfo{
			SpecName:              artifact.SpecName,
			EvidencePath:          artifact.EvidencePath,
			EvidenceExists:        artifact.EvidenceExists,
			TranscriptPath:        artifact.TranscriptPath,
			TranscriptExists:      artifact.TranscriptExists,
			ObjectiveLedgerPath:   artifact.ObjectiveLedgerPath,
			ObjectiveLedgerExists: artifact.ObjectiveLedgerExists,
			WorkspacePath:         artifact.WorkspacePath,
			WorkspaceExists:       artifact.WorkspaceExists,
		})
	}
	for _, spec := range d.Specs {
		failures := cloneCounts(spec.Failures)
		if len(failures) == 0 {
			failures = nil
		}
		out.Specs = append(out.Specs, sessionSpecAnalysis{
			Name:            firstNonEmpty(spec.Name, spec.DirName),
			Result:          spec.Result,
			Completion:      spec.CompletionReason,
			CostUSD:         spec.Totals.CostUSD,
			CostUnavailable: spec.Totals.CostUnavailable,
			InputTokens:     spec.Totals.InputTokens,
			OutputTokens:    spec.Totals.OutputTokens,
			Rounds:          spec.Totals.Rounds,
			Failures:        failures,
		})
	}
	return out
}

func analyzeSessionSet(analyses []sessionAnalysis) sessionSetAnalysis {
	out := sessionSetAnalysis{
		Count:       len(analyses),
		Sessions:    analyses,
		Results:     map[string]int{},
		Failures:    map[string]int{},
		Budgets:     map[string]int{},
		StopReasons: map[string]int{},
	}
	var costs []float64
	var inputTokens []float64
	var outputTokens []float64
	var rounds []float64
	for _, analysis := range analyses {
		if analysis.CostUnavailable {
			out.CostUnavailable = true
			out.CostUnavailableN++
		} else {
			out.TotalCostUSD += analysis.CostUSD
			costs = append(costs, analysis.CostUSD)
		}
		out.TotalInputTokens += analysis.InputTokens
		out.TotalOutputTokens += analysis.OutputTokens
		out.TotalRounds += analysis.Rounds
		result := firstNonEmpty(analysis.Result, "unknown")
		out.Results[result]++
		inputTokens = append(inputTokens, float64(analysis.InputTokens))
		outputTokens = append(outputTokens, float64(analysis.OutputTokens))
		rounds = append(rounds, float64(analysis.Rounds))
		mergeCounts(out.Failures, analysis.Failures)
		mergeCounts(out.Budgets, analysis.Budgets)
		mergeCounts(out.StopReasons, analysis.StopReasons)
	}
	if len(out.Budgets) == 0 {
		out.Budgets = nil
	}
	if len(out.StopReasons) == 0 {
		out.StopReasons = nil
	}
	if out.Count > 0 {
		out.PassRate = float64(out.Results["success"]) / float64(out.Count)
	}
	out.Distributions = sessionAnalysisDistribution{
		CostUSD:      distribution(costs),
		InputTokens:  distribution(inputTokens),
		OutputTokens: distribution(outputTokens),
		Rounds:       distribution(rounds),
	}
	return out
}

func analyzeSessionEvents(session *sessionapi.Session, events []sessionapi.SessionEvent) sessionAnalysis {
	return analyzeSessionDiagnostics(sessionapi.DiagnosticsFromEvents(session, events))
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

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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

func cloneCounts(in map[string]int) map[string]int {
	out := map[string]int{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func mergeCounts(total map[string]int, in map[string]int) {
	for key, value := range in {
		total[key] += value
	}
}

func distribution(values []float64) numericDistribution {
	if len(values) == 0 {
		return numericDistribution{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var sum float64
	for _, value := range sorted {
		sum += value
	}
	return numericDistribution{
		Count: len(sorted),
		Min:   sorted[0],
		P50:   percentile(sorted, 0.50),
		P95:   percentile(sorted, 0.95),
		Max:   sorted[len(sorted)-1],
		Avg:   sum / float64(len(sorted)),
	}
}

func percentile(sorted []float64, pct float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := int(math.Ceil(pct*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
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
