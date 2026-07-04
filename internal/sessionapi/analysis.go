package sessionapi

import (
	"math"
	"sort"
)

type SessionAnalysis struct {
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
	Retries          []SessionRetrySummary            `json:"retries,omitempty"`
	Errors           []SessionErrorSummary            `json:"errors,omitempty"`
	OutsideWorkspace []SessionOutsideWorkspaceSummary `json:"outside_workspace_access,omitempty"`
	Artifacts        []SessionArtifactInfo            `json:"artifacts,omitempty"`
	Specs            []SessionSpecAnalysis            `json:"specs,omitempty"`
}

type SessionSetAnalysis struct {
	Count             int                         `json:"count"`
	Sessions          []SessionAnalysis           `json:"sessions"`
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
	Distributions     SessionAnalysisDistribution `json:"distributions"`
}

type SessionAnalysisDistribution struct {
	CostUSD      NumericDistribution `json:"cost_usd"`
	InputTokens  NumericDistribution `json:"input_tokens"`
	OutputTokens NumericDistribution `json:"output_tokens"`
	Rounds       NumericDistribution `json:"rounds"`
}

type NumericDistribution struct {
	Count int     `json:"count"`
	Min   float64 `json:"min"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	Max   float64 `json:"max"`
	Avg   float64 `json:"avg"`
}

type SessionSpecAnalysis struct {
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

type SessionRetrySummary struct {
	SpecName           string `json:"spec_name,omitempty"`
	TurnID             string `json:"turn_id,omitempty"`
	Sequence           int    `json:"sequence,omitempty"`
	Attempt            int    `json:"attempt,omitempty"`
	DelayMS            int    `json:"delay_ms,omitempty"`
	ErrorCode          string `json:"error_code,omitempty"`
	ProviderStatusCode int    `json:"provider_status_code,omitempty"`
}

type SessionErrorSummary struct {
	SpecName  string `json:"spec_name,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Sequence  int    `json:"sequence,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

type SessionOutsideWorkspaceSummary struct {
	SpecName string `json:"spec_name,omitempty"`
	TurnID   string `json:"turn_id,omitempty"`
	Action   string `json:"action,omitempty"`
	Path     string `json:"path,omitempty"`
	Write    bool   `json:"write,omitempty"`
}

type SessionArtifactInfo struct {
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

func AnalyzeSessionDiagnostics(d *SessionDiagnosticsResponse) SessionAnalysis {
	if d == nil {
		return SessionAnalysis{Failures: map[string]int{}}
	}
	out := SessionAnalysis{
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
		out.Retries = append(out.Retries, SessionRetrySummary{
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
		out.Errors = append(out.Errors, SessionErrorSummary{
			SpecName:  errInfo.SpecName,
			TurnID:    errInfo.TurnID,
			Sequence:  errInfo.Sequence,
			ErrorCode: errInfo.ErrorCode,
			Error:     errInfo.Error,
		})
	}
	for _, access := range d.OutsideWorkspace {
		out.OutsideWorkspace = append(out.OutsideWorkspace, SessionOutsideWorkspaceSummary{
			SpecName: access.SpecName,
			TurnID:   access.TurnID,
			Action:   access.Action,
			Path:     access.Path,
			Write:    access.Write,
		})
	}
	for _, artifact := range d.Artifacts {
		out.Artifacts = append(out.Artifacts, SessionArtifactInfo{
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
		out.Specs = append(out.Specs, SessionSpecAnalysis{
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

func AnalyzeSessionSet(analyses []SessionAnalysis) SessionSetAnalysis {
	out := SessionSetAnalysis{
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
	out.Distributions = SessionAnalysisDistribution{
		CostUSD:      distribution(costs),
		InputTokens:  distribution(inputTokens),
		OutputTokens: distribution(outputTokens),
		Rounds:       distribution(rounds),
	}
	return out
}

func AnalyzeSessionEvents(session *Session, events []SessionEvent) SessionAnalysis {
	return AnalyzeSessionDiagnostics(DiagnosticsFromEvents(session, events))
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

func distribution(values []float64) NumericDistribution {
	if len(values) == 0 {
		return NumericDistribution{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var sum float64
	for _, value := range sorted {
		sum += value
	}
	return NumericDistribution{
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
