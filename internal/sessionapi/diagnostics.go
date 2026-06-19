package sessionapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func buildSessionDiagnostics(session *Session, events []SessionEvent) *SessionDiagnosticsResponse {
	diagnostics := &SessionDiagnosticsResponse{
		Failures:         map[string]int{},
		BudgetExceeded:   map[string]int{},
		StopReasons:      map[string]int{},
		SessionLogEvents: map[string]int{},
		Retries:          []SessionRetryDiagnostics{},
		Errors:           []SessionErrorDiagnostics{},
		OutsideWorkspace: []SessionOutsideWorkspaceAccessDiagnostics{},
		Artifacts:        []SessionArtifactDiagnostics{},
		Specs:            []SessionSpecDiagnostics{},
	}
	if session == nil {
		return diagnostics
	}

	diagnostics.SessionID = session.SessionID
	diagnostics.Status = session.Status
	diagnostics.Runtime = session.Runtime
	diagnostics.SessionKind = session.SessionKind
	diagnostics.ParentSessionID = session.ParentSessionID
	diagnostics.Result = session.Result
	diagnostics.CompletionReason = session.CompletionReason
	diagnostics.Error = session.Error
	diagnostics.Config = session.Config
	diagnostics.Limits = budgetDiagnostics(session.Config)
	diagnostics.Totals = totalsFromSession(session)

	specs := map[string]*SessionSpecDiagnostics{}
	primarySpecKeys := map[string]struct{}{}
	for _, spec := range session.Specs {
		name := diagnosticSpecName(spec)
		dirName := stringValue(spec.DirName)
		specDiagnostics := &SessionSpecDiagnostics{
			Name:             name,
			DirName:          dirName,
			CompletionReason: stringValue(spec.CompletionReason),
			Totals:           totalsFromSpec(spec),
			CurrentRound:     spec.CurrentRound,
			CurrentRole:      spec.CurrentRole,
			Failures:         map[string]int{},
		}
		primaryKey := diagnosticSpecKey(name, dirName)
		specs[primaryKey] = specDiagnostics
		primarySpecKeys[primaryKey] = struct{}{}
		if dirName != "" && dirName != primaryKey {
			specs[dirName] = specDiagnostics
		}
		diagnostics.Artifacts = append(diagnostics.Artifacts, artifactDiagnostics(spec))
	}

	for _, event := range events {
		specName := eventDiagnosticsSpecName(event)
		spec := specs[diagnosticSpecKey(specName, specName)]
		if spec == nil && specName != "" {
			spec = &SessionSpecDiagnostics{Name: specName, DirName: specName, Failures: map[string]int{}}
			specs[diagnosticSpecKey(specName, specName)] = spec
			primarySpecKeys[diagnosticSpecKey(specName, specName)] = struct{}{}
		}
		data := event.Data
		switch event.Event {
		case "game_end":
			result := stringFromMap(data, "game_result")
			completion := stringFromMap(data, "completion_reason")
			errText := stringFromMap(data, "error")
			if diagnostics.Result == nil && result != "" {
				diagnostics.Result = &result
			}
			if diagnostics.CompletionReason == nil && completion != "" {
				diagnostics.CompletionReason = &completion
			}
			if spec != nil {
				spec.Result = result
				spec.CompletionReason = completion
				spec.Totals = SessionDiagnosticsTotals{
					CostUSD:         floatFromMap(data, "total_cost_usd"),
					CostUnavailable: boolFromMap(data, "cost_unavailable"),
					InputTokens:     intFromMap(data, "total_input_tokens"),
					OutputTokens:    intFromMap(data, "total_output_tokens"),
					Rounds:          intFromMap(data, "prover_rounds") + intFromMap(data, "verifier_rounds"),
				}
			}
			if errText != "" {
				addDiagnosticsFailure(diagnostics.Failures, spec, ClassifyFailure(errText))
			} else if result == "failure" && len(diagnostics.Failures) == 0 {
				addDiagnosticsFailure(diagnostics.Failures, spec, "goal_failure")
			}
		case "budget_exceeded":
			budget := stringFromMap(data, "budget")
			if budget == "" {
				budget = "unknown"
			}
			diagnostics.BudgetExceeded[budget]++
			addDiagnosticsFailure(diagnostics.Failures, spec, "task_budget")
		case "agent_failure_recoverable", "game_error", "error":
			addDiagnosticsFailure(diagnostics.Failures, spec, ClassifyFailure(firstDiagnosticsNonEmpty(stringFromMap(data, "error_code"), stringFromMap(data, "error"))))
		case "agent_complete":
			if event.Role != nil && *event.Role == "verifier" &&
				stringFromMap(data, "status") == "CONTINUE" && stringFromMap(data, "error") == "" {
				addDiagnosticsFailure(diagnostics.Failures, spec, "verifier_rejection")
			}
		}
	}
	if session.Error != nil && *session.Error != "" && len(diagnostics.Failures) == 0 {
		addDiagnosticsFailure(diagnostics.Failures, nil, ClassifyFailure(*session.Error))
	}

	keys := make([]string, 0, len(specs))
	for key := range specs {
		if _, ok := primarySpecKeys[key]; !ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		spec := *specs[key]
		if len(spec.Failures) == 0 {
			spec.Failures = nil
		}
		diagnostics.Specs = append(diagnostics.Specs, spec)
	}
	return diagnostics
}

func appendSessionLogDiagnostics(diagnostics *SessionDiagnosticsResponse, sessionDir string) error {
	if diagnostics == nil || sessionDir == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(sessionDir, "specs", "*", "turns", "*", "session.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	for _, path := range matches {
		if err := readSessionLogDiagnostics(diagnostics, path); err != nil {
			diagnostics.ScanErrors = append(diagnostics.ScanErrors, err.Error())
		}
	}
	return nil
}

type diagnosticSessionLogEvent struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

func readSessionLogDiagnostics(diagnostics *SessionDiagnosticsResponse, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read session log %s: %w", path, err)
	}
	defer f.Close()

	specName, turnID := sessionLogContext(path)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event diagnosticSessionLogEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type != "" {
			diagnostics.SessionLogEvents[event.Type]++
		}
		switch event.Type {
		case "retry":
			diagnostics.Retries = append(diagnostics.Retries, SessionRetryDiagnostics{
				SpecName:           specName,
				TurnID:             turnID,
				Sequence:           intFromMap(event.Data, "sequence"),
				Attempt:            intFromMap(event.Data, "attempt"),
				DelayMS:            intFromMap(event.Data, "delay_ms"),
				ErrorCode:          stringFromMap(event.Data, "error_code"),
				Error:              stringFromMap(event.Data, "error"),
				ProviderStatusCode: intFromMap(event.Data, "provider_status_code"),
			})
		case "error":
			errorCode := stringFromMap(event.Data, "error_code")
			errorText := firstDiagnosticsNonEmpty(errorCode, stringFromMap(event.Data, "error"))
			diagnostics.Errors = append(diagnostics.Errors, SessionErrorDiagnostics{
				SpecName:           specName,
				TurnID:             turnID,
				Sequence:           intFromMap(event.Data, "sequence"),
				ErrorCode:          errorCode,
				Error:              stringFromMap(event.Data, "error"),
				Retryable:          boolPtrFromAny(event.Data["retryable"]),
				ProviderStatusCode: intFromMap(event.Data, "provider_status_code"),
			})
			addDiagnosticsFailure(diagnostics.Failures, diagnosticsSpecByName(diagnostics, specName), ClassifyFailure(errorText))
		case "model_response":
			if stopReason := stringFromMap(event.Data, "stop_reason"); stopReason != "" {
				diagnostics.StopReasons[stopReason]++
			}
		case "tool_result":
			errorCode := stringFromMap(event.Data, "error_code")
			if boolFromAny(event.Data["is_error"]) && errorCode != "" {
				diagnostics.Errors = append(diagnostics.Errors, SessionErrorDiagnostics{
					SpecName:  specName,
					TurnID:    turnID,
					ErrorCode: errorCode,
					Error:     "tool_result:" + stringFromMap(event.Data, "tool_name"),
				})
				addDiagnosticsFailure(diagnostics.Failures, diagnosticsSpecByName(diagnostics, specName), ClassifyFailure(errorCode))
			}
		case "outside_workspace_access":
			diagnostics.OutsideWorkspace = append(diagnostics.OutsideWorkspace, SessionOutsideWorkspaceAccessDiagnostics{
				SpecName: specName,
				TurnID:   turnID,
				Action:   stringFromMap(event.Data, "action"),
				Path:     stringFromMap(event.Data, "path"),
				Write:    boolFromAny(event.Data["write"]),
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan session log %s: %w", path, err)
	}
	return nil
}

func sessionLogContext(path string) (string, string) {
	turnDir := filepath.Dir(path)
	turnID := filepath.Base(turnDir)
	specName := filepath.Base(filepath.Dir(filepath.Dir(turnDir)))
	return specName, turnID
}

func diagnosticsSpecByName(diagnostics *SessionDiagnosticsResponse, name string) *SessionSpecDiagnostics {
	if diagnostics == nil || name == "" {
		return nil
	}
	for i := range diagnostics.Specs {
		spec := &diagnostics.Specs[i]
		if spec.Name == name || spec.DirName == name {
			return spec
		}
	}
	return nil
}

func budgetDiagnostics(config map[string]any) SessionBudgetDiagnostics {
	if config == nil {
		return SessionBudgetDiagnostics{}
	}
	return SessionBudgetDiagnostics{
		MaxCostUSD:        floatPtrFromAny(config["max_cost_usd"]),
		MaxRounds:         intValue(config["max_rounds"]),
		MaxDurationSec:    intValue(config["max_duration_sec"]),
		MaxInputTokens:    intValue(config["max_input_tokens"]),
		MaxOutputTokens:   intValue(config["max_output_tokens"]),
		MaxToolLoops:      intValue(config["max_tool_loops"]),
		AgentTimeoutSec:   intValue(config["agent_timeout_sec"]),
		SafeWritePrefixes: stringSliceValue(config["safe_write_prefixes"]),
	}
}

func totalsFromSession(session *Session) SessionDiagnosticsTotals {
	return SessionDiagnosticsTotals{
		CostUSD:          floatValue(session.TotalCostUSD),
		CostUnavailable:  boolValue(session.CostUnavailable),
		InputTokens:      intPtrValue(session.TotalInputTokens),
		OutputTokens:     intPtrValue(session.TotalOutputTokens),
		CacheReadTokens:  intPtrValue(session.TotalCacheReadTokens),
		CacheWriteTokens: intPtrValue(session.TotalCacheCreateTokens),
		Rounds:           intPtrValue(session.RoundCount),
	}
}

func totalsFromSpec(spec SessionSpec) SessionDiagnosticsTotals {
	return SessionDiagnosticsTotals{
		CostUSD:          floatValue(spec.TotalCostUSD),
		CostUnavailable:  boolValue(spec.CostUnavailable),
		InputTokens:      intPtrValue(spec.TotalInputTokens),
		OutputTokens:     intPtrValue(spec.TotalOutputTokens),
		CacheReadTokens:  intPtrValue(spec.TotalCacheReadTokens),
		CacheWriteTokens: intPtrValue(spec.TotalCacheCreateTokens),
		Rounds:           intPtrValue(spec.RoundCount),
	}
}

func artifactDiagnostics(spec SessionSpec) SessionArtifactDiagnostics {
	return SessionArtifactDiagnostics{
		SpecName:              stringValue(spec.Name),
		SpecDirName:           stringValue(spec.DirName),
		EvidencePath:          stringValue(spec.EvidencePath),
		EvidenceExists:        boolValue(spec.EvidenceExists),
		TranscriptPath:        stringValue(spec.TranscriptPath),
		TranscriptExists:      boolValue(spec.TranscriptExists),
		ObjectiveLedgerPath:   stringValue(spec.ObjectiveLedgerPath),
		ObjectiveLedgerExists: boolValue(spec.ObjectiveLedgerExists),
		WorkspacePath:         stringValue(spec.WorkspacePath),
		WorkspaceExists:       boolValue(spec.WorkspaceExists),
	}
}

func addDiagnosticsFailure(total map[string]int, spec *SessionSpecDiagnostics, category string) {
	if category == "" {
		category = "unknown"
	}
	total[category]++
	if spec != nil {
		if spec.Failures == nil {
			spec.Failures = map[string]int{}
		}
		spec.Failures[category]++
	}
}

// ClassifyFailure maps a turn/game error string to a failure-taxonomy category.
// It is the single classifier shared by the diagnostics endpoint and the
// `telos analyze` CLI so the two cannot report divergent categories.
func ClassifyFailure(errText string) string {
	lower := strings.ToLower(strings.TrimSpace(errText))
	switch {
	case lower == "":
		return "unknown"
	case strings.Contains(lower, "benchmark_verifier") ||
		strings.Contains(lower, "benchmark verifier") ||
		strings.Contains(lower, "official verifier"):
		return "benchmark_verifier_failure"
	case strings.Contains(lower, "runtime_budget_exhausted") || strings.Contains(lower, "budget exceeded"):
		return "task_budget"
	case strings.Contains(lower, "provider_") || strings.Contains(lower, "rate_limited") || strings.Contains(lower, "context_limit"):
		return "provider"
	case strings.Contains(lower, "tool_") || strings.Contains(lower, "local_timeout") || strings.Contains(lower, "local_interrupted"):
		return "tool"
	case strings.Contains(lower, "agent_protocol"):
		return "protocol"
	case strings.Contains(lower, "agent_incomplete") || strings.Contains(lower, "tool_loop_exceeded"):
		return "agent_incomplete"
	case strings.Contains(lower, "stopped"):
		return "stopped"
	default:
		return "agent_failure"
	}
}

func eventDiagnosticsSpecName(event SessionEvent) string {
	if event.SpecName != nil && *event.SpecName != "" {
		return *event.SpecName
	}
	if event.SpecDirName != nil && *event.SpecDirName != "" {
		return *event.SpecDirName
	}
	return ""
}

func firstDiagnosticsNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func diagnosticSpecName(spec SessionSpec) string {
	if spec.Name != nil && *spec.Name != "" {
		return *spec.Name
	}
	return stringValue(spec.DirName)
}

func diagnosticSpecKey(name, dirName string) string {
	if name != "" {
		return name
	}
	return dirName
}

func stringFromMap(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func intFromMap(data map[string]any, key string) int {
	if data == nil {
		return 0
	}
	return intValue(data[key])
}

func floatFromMap(data map[string]any, key string) float64 {
	if data == nil {
		return 0
	}
	return floatAnyValue(data[key])
}

func boolFromMap(data map[string]any, key string) bool {
	if data == nil {
		return false
	}
	return boolFromAny(data[key])
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

func floatAnyValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return 0
}

func floatPtrFromAny(value any) *float64 {
	switch v := value.(type) {
	case float64:
		return &v
	case int:
		f := float64(v)
		return &f
	case int64:
		f := float64(v)
		return &f
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return &f
		}
	}
	return nil
}

func stringSliceValue(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func intPtrValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func floatValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func boolFromAny(value any) bool {
	v, _ := value.(bool)
	return v
}
