package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

func TestReorderInterspersedFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")
	fs.String("workspace", "", "")
	fs.Int("until", 0, "")

	got := reorderInterspersedFlags(fs, []string{
		"SPEC.md",
		"--json",
		"--workspace",
		"/tmp/ws",
		"--until=3",
	})
	want := []string{
		"--json",
		"--workspace",
		"/tmp/ws",
		"--until=3",
		"SPEC.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTopLevelUsageMentionsHelpAndVersion(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	text := out.String()
	for _, want := range []string{
		"usage: telos <command> [args]",
		"--help",
		"version            Show version",
		"--version",
		"telos <command> --help",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing %q:\n%s", want, text)
		}
	}
}

func TestPrintPlanPreviewLocal(t *testing.T) {
	compiled := &spec.CompiledEnvironment{
		Environment: &spec.EnvironmentSpec{Name: "hello-service"},
		ContentHash: "8a8f0c21",
		Skills: []*spec.Skill{
			{Name: "verify-engineering"},
		},
	}

	var out bytes.Buffer
	printPlanPreview(&out, compiled, "./SPEC.md", "local", "root", "")
	text := out.String()
	for _, want := range []string{
		"Spec      hello-service",
		"Platform  local",
		"Lineage   root",
		"Mutates   no",
		"Path      ./SPEC.md",
		"Hash      8a8f0c21",
		"Skills    verify-engineering",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan output missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Target", "Namespace", "Plan for", "No sessions"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("plan output should not contain %q:\n%s", notWant, text)
		}
	}
}

func TestPrintPlanPreviewCloud(t *testing.T) {
	compiled := &spec.CompiledEnvironment{
		Environment: &spec.EnvironmentSpec{Name: "gitea"},
		Namespace:   "ns-gitea",
		ContentHash: "8a8f0c21",
		Skills: []*spec.Skill{
			{Name: "verify-engineering"},
			{Name: "verify-quality"},
		},
	}

	var out bytes.Buffer
	printPlanPreview(&out, compiled, "./SPEC.md", "cloud", "root", "env_123")
	text := out.String()
	for _, want := range []string{
		"Spec      gitea",
		"Platform  cloud",
		"Lineage   root",
		"Mutates   no",
		"Path      ./SPEC.md",
		"Namespace ns-gitea",
		"Hash      8a8f0c21",
		"Skills    verify-engineering, verify-quality",
		"Target    env_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintSessionDescriptionShowsRuntimeAndLedger(t *testing.T) {
	status := sessionapi.StatusCompleted
	kind := sessionapi.KindTask
	created := "2026-06-19T10:00:00.000Z"
	finished := "2026-06-19T10:05:00.000Z"
	specName := "demo"
	workspacePath := filepath.Join(t.TempDir(), "workspace.tar.gz")
	evidencePath := filepath.Join(t.TempDir(), "evidence.jsonl")
	transcriptPath := filepath.Join(t.TempDir(), "transcript.md")
	ledgerPath := filepath.Join(t.TempDir(), "objective-ledger.json")
	exists := true
	totalInput := 1234
	totalOutput := 567
	totalCacheRead := 89
	totalCacheWrite := 10
	errText := "runtime_budget_exhausted:max_rounds"
	errCode := "runtime_budget_exhausted"
	session := sessionapi.Session{
		SessionID:              "local_123",
		SessionKind:            &kind,
		SpecName:               &specName,
		Status:                 status,
		CreatedAt:              &created,
		FinishedAt:             &finished,
		Runtime:                sessionapi.RuntimeLocal,
		Config:                 map[string]any{"model": "test/model", "thinking": "high", "max_rounds": 9.0, "max_input_tokens": 100000.0, "max_output_tokens": 20000.0, "max_tool_loops": 55.0, "agent_timeout_sec": 120.0},
		Error:                  &errText,
		ErrorCode:              &errCode,
		TotalInputTokens:       &totalInput,
		TotalOutputTokens:      &totalOutput,
		TotalCacheReadTokens:   &totalCacheRead,
		TotalCacheCreateTokens: &totalCacheWrite,
		Specs: []sessionapi.SessionSpec{{
			Name:                  &specName,
			WorkspacePath:         &workspacePath,
			WorkspaceExists:       &exists,
			EvidencePath:          &evidencePath,
			EvidenceExists:        &exists,
			TranscriptPath:        &transcriptPath,
			TranscriptExists:      &exists,
			ObjectiveLedgerPath:   &ledgerPath,
			ObjectiveLedgerExists: &exists,
		}},
	}

	var out bytes.Buffer
	printSessionDescription(&out, session)
	text := out.String()
	for _, want := range []string{
		"Runtime",
		"model          test/model",
		"thinking       high",
		"budgets        rounds 9, input 100000, output 20000, tool-loops 55, agent-timeout 120s",
		"error code     runtime_budget_exhausted",
		"tokens         input 1234, output 567, cache-read 89, cache-write 10",
		"demo ledger    file://",
		"objective-ledger.json",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("describe output missing %q:\n%s", want, text)
		}
	}
}

func TestReplayTargetDirectPathInfersRoleAndValidatesProtocol(t *testing.T) {
	dir := t.TempDir()
	turnDir := filepath.Join(dir, "turns", "0001-prover")
	if err := os.MkdirAll(turnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(turnDir, "session.jsonl")
	writeReplayLog(t, logPath, "Implement code in the workspace.", "Done.\n\n<progress_update>Updated code.</progress_update>", true)

	reports, err := replayTarget(logPath, "")
	if err != nil {
		t.Fatalf("replayTarget: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports: got %d", len(reports))
	}
	if reports[0].Role != "prover" || !reports[0].Report.ProtocolOK {
		t.Fatalf("report: %+v", reports[0])
	}
	if reports[0].Report.ToolCalls != 1 || reports[0].Report.ToolResults != 1 {
		t.Fatalf("tool counts: %+v", reports[0].Report)
	}
}

func TestReplayTargetDirectPathRequiresRoleWhenNotInferable(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "session.jsonl")
	writeReplayLog(t, logPath, "Implement code in the workspace.", "Done.", false)

	_, err := replayTarget(logPath, "")
	if err == nil {
		t.Fatal("expected missing role error")
	}
	if !strings.Contains(err.Error(), "--role is required") {
		t.Fatalf("unexpected error: %v", err)
	}

	reports, err := replayTarget(logPath, "prover")
	if err != nil {
		t.Fatalf("replayTarget with role: %v", err)
	}
	if reports[0].Report.ProtocolOK || reports[0].Report.ProtocolError != "missing_progress_update" {
		t.Fatalf("expected missing progress update, got %+v", reports[0].Report)
	}
}

func TestReplayLocalSessionDiscoversTurnLogs(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "local_123")
	proverPath := filepath.Join(sessionDir, "specs", "demo", "turns", "0001-prover", "session.jsonl")
	verifierPath := filepath.Join(sessionDir, "specs", "demo", "turns", "0002-verifier", "session.jsonl")
	writeReplayLog(t, proverPath, "Implement code in the workspace.", "Done.\n\n<progress_update>Updated code.</progress_update>", true)
	writeReplayLog(t, verifierPath, "Verify code.", "Looks good.\n\n<status>CONCEDE</status>", false)
	session := &sessionapi.Session{SessionID: "local_123", SessionDir: &sessionDir}

	reports, err := replayLocalSession(session)
	if err != nil {
		t.Fatalf("replayLocalSession: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports: got %d", len(reports))
	}
	if reports[0].Role != "prover" || reports[1].Role != "verifier" {
		t.Fatalf("roles: %+v", reports)
	}
	var out bytes.Buffer
	printReplayReports(&out, reports)
	text := out.String()
	for _, want := range []string{"Replay", "prover", "verifier", "ok", "0001-prover", "0002-verifier"} {
		if !strings.Contains(text, want) {
			t.Fatalf("replay output missing %q:\n%s", want, text)
		}
	}
}

func TestReplayReportsToolTraceFailuresAndTruncation(t *testing.T) {
	dir := t.TempDir()
	turnDir := filepath.Join(dir, "turns", "0001-prover")
	logPath := filepath.Join(turnDir, "session.jsonl")
	writeReplayEvents(t, logPath, []map[string]any{
		{"type": "session", "version": 1},
		{"type": "message", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "Implement code."}}}},
		{"type": "tool_call", "data": map[string]any{"tool_call_id": "call_1", "tool_name": "bash", "arguments": `{"command":"false"}`}},
		{"type": "tool_result", "data": map[string]any{"tool_call_id": "call_1", "tool_name": "bash", "is_error": true, "exit_code": 2, "truncated": true}},
		{"type": "message", "message": map[string]any{"role": "toolResult", "toolCallId": "call_1", "toolName": "bash", "isError": true, "content": []map[string]any{{"type": "text", "text": "tool: bash\nok: false\nexit_code: 2\nstdout_truncated: true\n"}}}},
		{"type": "message", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "Done.\n<progress_update>checked command</progress_update>"}}}},
	})

	reports, err := replayTarget(logPath, "")
	if err != nil {
		t.Fatalf("replayTarget: %v", err)
	}
	report := reports[0].Report
	if !report.ProtocolOK {
		t.Fatalf("protocol should pass, got %+v", report)
	}
	if report.ToolErrors != 1 || report.ToolNonzeroExits != 1 || report.ToolTruncated != 1 {
		t.Fatalf("tool trace counters: %+v", report)
	}

	var out bytes.Buffer
	printReplayReports(&out, reports)
	text := out.String()
	for _, want := range []string{"TOOL_ERR", "EXIT", "TRUNC", "1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("replay output missing %q:\n%s", want, text)
		}
	}
}

func TestReplayFailsOnToolTraceMismatch(t *testing.T) {
	dir := t.TempDir()
	turnDir := filepath.Join(dir, "turns", "0001-prover")
	logPath := filepath.Join(turnDir, "session.jsonl")
	writeReplayEvents(t, logPath, []map[string]any{
		{"type": "session", "version": 1},
		{"type": "message", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "Implement code."}}}},
		{"type": "tool_call", "data": map[string]any{"tool_call_id": "call_1", "tool_name": "read_file", "arguments": `{"path":"main.go"}`}},
		{"type": "message", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "Done.\n<progress_update>stopped early</progress_update>"}}}},
	})

	reports, err := replayTarget(logPath, "")
	if err != nil {
		t.Fatalf("replayTarget: %v", err)
	}
	report := reports[0].Report
	if report.ProtocolOK || report.ProtocolError != "tool_trace_mismatch" || report.UnmatchedToolCalls != 1 {
		t.Fatalf("expected tool trace mismatch, got %+v", report)
	}
}

func TestAnalyzeSessionEventsBuildsFailureTaxonomy(t *testing.T) {
	specName := "demo"
	roleVerifier := "verifier"
	roundOne := 1
	cost := 1.25
	input := 1000
	output := 500
	rounds := 2
	completion := "runtime_budget_exhausted"
	costUnavailable := true
	session := &sessionapi.Session{
		SessionID:         "local_123",
		Status:            sessionapi.StatusFailed,
		Config:            map[string]any{},
		TotalCostUSD:      &cost,
		CostUnavailable:   &costUnavailable,
		TotalInputTokens:  &input,
		TotalOutputTokens: &output,
		RoundCount:        &rounds,
		CompletionReason:  &completion,
		Specs: []sessionapi.SessionSpec{{
			Name: &specName,
		}},
	}
	events := []sessionapi.SessionEvent{
		{Event: "agent_complete", SpecName: &specName, Round: &roundOne, Role: &roleVerifier, Data: map[string]any{"status": "CONTINUE"}},
		{Event: "agent_failure_recoverable", SpecName: &specName, Data: map[string]any{"error_code": "provider_rate_limited", "error": "retry later"}},
		{Event: "agent_failure_recoverable", SpecName: &specName, Data: map[string]any{"error_code": "agent_protocol", "error": "missing status"}},
		{Event: "game_error", SpecName: &specName, Data: map[string]any{"error": "official verifier rejected benchmark output"}},
		{Event: "budget_exceeded", SpecName: &specName, Data: map[string]any{"budget": "max_input_tokens"}},
		{Event: "cost_cap_unenforceable", SpecName: &specName, Data: map[string]any{"max_cost_usd": 10.0}},
		{Event: "game_end", SpecName: &specName, Data: map[string]any{
			"game_result":         "failure",
			"completion_reason":   "runtime_budget_exhausted",
			"prover_rounds":       1.0,
			"verifier_rounds":     1.0,
			"total_cost_usd":      1.25,
			"cost_unavailable":    true,
			"total_input_tokens":  1000.0,
			"total_output_tokens": 500.0,
			"error":               "runtime_budget_exhausted:max_input_tokens",
		}},
	}

	analysis := analyzeSessionEvents(session, events)
	diagnosticsAnalysis := analyzeSessionDiagnostics(sessionapi.DiagnosticsFromEvents(session, events))
	if !reflect.DeepEqual(analysis, diagnosticsAnalysis) {
		t.Fatalf("events and diagnostics analysis diverged\nevents: %#v\ndiagnostics: %#v", analysis, diagnosticsAnalysis)
	}
	if analysis.Failures["verifier_rejection"] != 1 {
		t.Fatalf("verifier rejection count: %#v", analysis.Failures)
	}
	if analysis.Failures["provider"] != 1 {
		t.Fatalf("provider count: %#v", analysis.Failures)
	}
	if analysis.Failures["task_budget"] != 2 {
		t.Fatalf("task budget count: %#v", analysis.Failures)
	}
	if analysis.Failures["benchmark_verifier_failure"] != 1 {
		t.Fatalf("benchmark verifier count: %#v", analysis.Failures)
	}
	if analysis.Failures["protocol"] != 1 {
		t.Fatalf("protocol count: %#v", analysis.Failures)
	}
	if analysis.Budgets["max_input_tokens"] != 1 || analysis.Budgets["cost_cap_unenforceable"] != 1 {
		t.Fatalf("budget counts: %#v", analysis.Budgets)
	}
	if !analysis.CostUnavailable {
		t.Fatalf("cost unavailable should be carried through: %#v", analysis)
	}
	if len(analysis.Specs) != 1 || analysis.Specs[0].Failures["provider"] != 1 {
		t.Fatalf("spec analysis: %#v", analysis.Specs)
	}

	var out bytes.Buffer
	printSessionAnalysis(&out, analysis)
	text := out.String()
	for _, want := range []string{"$1.2500 (unavailable)", "Failure Taxonomy", "provider", "protocol", "task_budget", "benchmark_verifier_failure", "Budget Limits", "max_input_tokens", "cost_cap_unenforceable", "Specs"} {
		if !strings.Contains(text, want) {
			t.Fatalf("analysis output missing %q:\n%s", want, text)
		}
	}
}

func TestAnalyzeSessionDiagnosticsIncludesRetriesStopsAndArtifacts(t *testing.T) {
	result := "failure"
	completion := "runtime_budget_exhausted"
	diagnostics := &sessionapi.SessionDiagnosticsResponse{
		SessionID:        "local_diag",
		Status:           sessionapi.StatusFailed,
		Result:           &result,
		CompletionReason: &completion,
		Totals: sessionapi.SessionDiagnosticsTotals{
			CostUSD:         0.42,
			CostUnavailable: true,
			InputTokens:     1300,
			OutputTokens:    210,
			Rounds:          2,
		},
		Failures:         map[string]int{"provider": 1, "task_budget": 1},
		BudgetExceeded:   map[string]int{"max_input_tokens": 1},
		StopReasons:      map[string]int{"tool_calls": 1},
		SessionLogEvents: map[string]int{"tool_result": 2, "outside_workspace_access": 1},
		Retries: []sessionapi.SessionRetryDiagnostics{{
			SpecName:           "demo",
			TurnID:             "0001-prover",
			Sequence:           1,
			Attempt:            2,
			DelayMS:            250,
			ErrorCode:          "provider_rate_limited",
			ProviderStatusCode: 429,
		}},
		Errors: []sessionapi.SessionErrorDiagnostics{{
			SpecName:  "demo",
			TurnID:    "0001-prover",
			Sequence:  2,
			ErrorCode: "agent_incomplete",
			Error:     "agent_incomplete:max_output_tokens",
		}},
		OutsideWorkspace: []sessionapi.SessionOutsideWorkspaceAccessDiagnostics{{
			SpecName: "demo",
			TurnID:   "0001-prover",
			Action:   "write_file",
			Path:     "/tmp/telos-scratch/out.txt",
			Write:    true,
		}},
		Artifacts: []sessionapi.SessionArtifactDiagnostics{{
			SpecName:              "demo",
			EvidenceExists:        true,
			TranscriptExists:      true,
			ObjectiveLedgerExists: true,
			WorkspaceExists:       false,
		}},
		Specs: []sessionapi.SessionSpecDiagnostics{{
			Name:             "demo",
			Result:           "failure",
			CompletionReason: "runtime_budget_exhausted",
			Totals: sessionapi.SessionDiagnosticsTotals{
				CostUnavailable: true,
				InputTokens:     1300,
				OutputTokens:    210,
				Rounds:          2,
			},
			Failures: map[string]int{"provider": 1},
		}},
	}

	analysis := analyzeSessionDiagnostics(diagnostics)
	if analysis.StopReasons["tool_calls"] != 1 || analysis.SessionLogEvents["tool_result"] != 2 || len(analysis.Retries) != 1 || len(analysis.OutsideWorkspace) != 1 || len(analysis.Artifacts) != 1 {
		t.Fatalf("diagnostics analysis missing rich fields: %#v", analysis)
	}
	if !analysis.CostUnavailable || len(analysis.Specs) != 1 || !analysis.Specs[0].CostUnavailable {
		t.Fatalf("cost unavailable not surfaced: %#v", analysis)
	}
	var out bytes.Buffer
	printSessionAnalysis(&out, analysis)
	text := out.String()
	for _, want := range []string{"$0.4200 (unavailable)", "Stop Reasons", "tool_calls", "Session Log Events", "tool_result", "Retries", "provider_rate_limited", "Errors", "agent_incomplete", "Outside Workspace Access", "/tmp/telos-scratch/out.txt", "Artifacts", "demo"} {
		if !strings.Contains(text, want) {
			t.Fatalf("diagnostics output missing %q:\n%s", want, text)
		}
	}
}

func TestAnalyzeSessionSetBuildsBenchmarkDistributions(t *testing.T) {
	analyses := []sessionAnalysis{
		{
			SessionID:    "s1",
			Status:       "completed",
			Result:       "success",
			CostUSD:      0.10,
			InputTokens:  100,
			OutputTokens: 20,
			Rounds:       2,
			Failures:     map[string]int{},
			StopReasons:  map[string]int{"completed": 1},
		},
		{
			SessionID:    "s2",
			Status:       "failed",
			Result:       "failure",
			Completion:   "runtime_budget_exhausted",
			CostUSD:      0.50,
			InputTokens:  500,
			OutputTokens: 50,
			Rounds:       5,
			Failures:     map[string]int{"task_budget": 1},
			Budgets:      map[string]int{"max_rounds": 1},
			StopReasons:  map[string]int{"max_output_tokens": 1},
		},
		{
			SessionID:       "s3",
			Status:          "failed",
			Result:          "failure",
			CostUSD:         0.25,
			CostUnavailable: true,
			InputTokens:     300,
			OutputTokens:    30,
			Rounds:          3,
			Failures:        map[string]int{"provider": 1, "tool": 1, "protocol": 1, "benchmark_verifier_failure": 1, "verifier_rejection": 1},
			StopReasons:     map[string]int{"completed": 1},
		},
	}

	aggregate := analyzeSessionSet(analyses)
	if aggregate.Count != 3 || aggregate.TotalInputTokens != 900 || aggregate.TotalOutputTokens != 100 || aggregate.TotalRounds != 10 {
		t.Fatalf("aggregate totals: %#v", aggregate)
	}
	if aggregate.Results["success"] != 1 || aggregate.Results["failure"] != 2 {
		t.Fatalf("result counts: %#v", aggregate.Results)
	}
	if aggregate.PassRate < 0.333 || aggregate.PassRate > 0.334 {
		t.Fatalf("pass rate: got %.4f", aggregate.PassRate)
	}
	if aggregate.Failures["task_budget"] != 1 ||
		aggregate.Failures["provider"] != 1 ||
		aggregate.Failures["tool"] != 1 ||
		aggregate.Failures["protocol"] != 1 ||
		aggregate.Failures["benchmark_verifier_failure"] != 1 ||
		aggregate.Failures["verifier_rejection"] != 1 {
		t.Fatalf("failure counts: %#v", aggregate.Failures)
	}
	if aggregate.Budgets["max_rounds"] != 1 {
		t.Fatalf("budget counts: %#v", aggregate.Budgets)
	}
	if !aggregate.CostUnavailable || aggregate.CostUnavailableN != 1 {
		t.Fatalf("cost unavailable aggregate: %#v", aggregate)
	}
	if aggregate.StopReasons["completed"] != 2 {
		t.Fatalf("stop reasons: %#v", aggregate.StopReasons)
	}
	if aggregate.Distributions.Rounds.P50 != 3 || aggregate.Distributions.Rounds.P95 != 5 {
		t.Fatalf("round distribution: %#v", aggregate.Distributions.Rounds)
	}

	var out bytes.Buffer
	printSessionSetAnalysis(&out, aggregate)
	text := out.String()
	for _, want := range []string{"Benchmark Analysis", "pass rate", "33.3%", "cost unavailable", "1 session", "$0.2500 (unavailable)", "Results", "success", "failure", "Distributions", "input_tokens", "Failure Taxonomy", "provider", "tool", "protocol", "verifier_rejection", "benchmark_verifier_failure", "task_budget", "Sessions", "s2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("aggregate output missing %q:\n%s", want, text)
		}
	}
}

func TestBuildChildInspectionWritesMarkerAndReadyChecklist(t *testing.T) {
	root := t.TempDir()
	parentDir := filepath.Join(root, "local_parent")
	childDir := filepath.Join(root, "local_child")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := "local_parent"
	child := "local_child"
	specName := "demo"
	workspacePath := filepath.Join(childDir, "specs", "demo", "workspace.tar.gz")
	transcriptPath := filepath.Join(childDir, "specs", "demo", "transcript.md")
	evidencePath := filepath.Join(childDir, "specs", "demo", "evidence.jsonl")
	ledgerPath := filepath.Join(childDir, "specs", "demo", "objective-ledger.json")
	exists := true
	result := "completed"
	completion := "verifier_conceded"
	session := &sessionapi.Session{
		SessionID:        child,
		ParentSessionID:  &parent,
		SessionDir:       &childDir,
		Status:           sessionapi.StatusCompleted,
		Result:           &result,
		CompletionReason: &completion,
		Specs: []sessionapi.SessionSpec{{
			Name:                  &specName,
			WorkspacePath:         &workspacePath,
			WorkspaceExists:       &exists,
			TranscriptPath:        &transcriptPath,
			EvidencePath:          &evidencePath,
			ObjectiveLedgerPath:   &ledgerPath,
			ObjectiveLedgerExists: &exists,
		}},
	}

	report, err := buildChildInspection(session, nil)
	if err != nil {
		t.Fatalf("buildChildInspection: %v", err)
	}
	if !report.ReadyToReconcile {
		t.Fatalf("expected ready report: %+v", report)
	}
	if report.InspectionPath == "" {
		t.Fatal("expected inspection marker path")
	}
	if !strings.Contains(report.InspectionPath, filepath.Join("local_parent", "child-inspections", "local_child.json")) {
		t.Fatalf("inspection path: got %q", report.InspectionPath)
	}
	data, err := os.ReadFile(report.InspectionPath)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !strings.Contains(string(data), `"ready_to_reconcile": true`) {
		t.Fatalf("marker missing ready flag:\n%s", data)
	}
	var out bytes.Buffer
	printChildInspection(&out, report)
	text := out.String()
	for _, want := range []string{"Child Inspection", "ready          true", "workspace checkpoint exists", "Failure Taxonomy"} {
		if !strings.Contains(text, want) {
			t.Fatalf("inspection output missing %q:\n%s", want, text)
		}
	}
}

func TestBuildChildInspectionRejectsNonChildAndBlocksUnready(t *testing.T) {
	_, err := buildChildInspection(&sessionapi.Session{SessionID: "local_not_child"}, nil)
	if err == nil {
		t.Fatal("expected non-child session to fail")
	}
	if !strings.Contains(err.Error(), "not a child session") {
		t.Fatalf("unexpected error: %v", err)
	}
	parent := "local_parent"
	specName := "demo"
	missing := false
	session := &sessionapi.Session{
		SessionID:       "local_child",
		ParentSessionID: &parent,
		Status:          sessionapi.StatusRunning,
		Specs: []sessionapi.SessionSpec{{
			Name:            &specName,
			WorkspaceExists: &missing,
		}},
	}
	report, err := buildChildInspection(session, []sessionapi.SessionEvent{
		{Event: "agent_failure_recoverable", SpecName: &specName, Data: map[string]any{"error": "tool_timeout: test"}},
	})
	if err != nil {
		t.Fatalf("buildChildInspection: %v", err)
	}
	if report.ReadyToReconcile {
		t.Fatalf("running child without workspace should not be ready: %+v", report)
	}
	if report.Analysis.Failures["tool"] != 1 {
		t.Fatalf("expected tool failure: %#v", report.Analysis.Failures)
	}
}

func writeReplayLog(t *testing.T, path string, task string, final string, withTool bool) {
	t.Helper()
	events := []map[string]any{
		{"type": "session", "version": 1},
		{"type": "message", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": task}}}},
		{"type": "model_request", "data": map[string]any{"sequence": 1}},
		{"type": "model_response", "data": map[string]any{"sequence": 1}},
	}
	if withTool {
		events = append(events,
			map[string]any{"type": "tool_call", "data": map[string]any{"tool_call_id": "call_1", "tool_name": "read_file", "arguments": `{"path":"main.go"}`}},
			map[string]any{"type": "message", "message": map[string]any{"role": "toolResult", "toolCallId": "call_1", "toolName": "read_file", "content": []map[string]any{{"type": "text", "text": "ok"}}}},
		)
	}
	events = append(events, map[string]any{"type": "message", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": final}}}})
	writeReplayEvents(t, path, events)
}

func writeReplayEvents(t *testing.T, path string, events []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReorderInterspersedFlagsDashDash(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")

	got := reorderInterspersedFlags(fs, []string{"--json", "--", "-literal"})
	want := []string{"--json", "-literal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestFlagNamesSetUsesExplicitFlagsOnly(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("thinking", "medium", "")
	fs.Int("until", 0, "")
	fs.String("workspace", "", "")
	parseFlags(fs, []string{"--thinking", "medium", "SPEC.md"})

	if !flagNamesSet(fs, "thinking") {
		t.Fatal("expected explicitly passed --thinking to be detected")
	}
	if flagNamesSet(fs, "until", "workspace") {
		t.Fatal("defaulted flags should not count as explicitly set")
	}
}

func TestResolveLocalRunConfigUsesEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("max-tool-loops", 0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"SPEC.md"})

	t.Setenv("TELOS_WORKSPACE", "/tmp/telos-workspace")
	t.Setenv("TELOS_MODEL", "claude-test")
	t.Setenv("TELOS_THINKING", "high")
	t.Setenv("TELOS_MAX_COST_USD", "12.5")
	t.Setenv("TELOS_MAX_TOOL_LOOPS", "44")
	t.Setenv("TELOS_AGENT_TIMEOUT_SEC", "123")

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, budgetFlags{})
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.Workspace != "/tmp/telos-workspace" {
		t.Fatalf("workspace: got %q", cfg.Workspace)
	}
	if cfg.Model != "claude-test" || cfg.Thinking != "high" {
		t.Fatalf("model/thinking: got %q/%q", cfg.Model, cfg.Thinking)
	}
	if cfg.AgentTimeoutSec != 123 {
		t.Fatalf("timeout: got %d", cfg.AgentTimeoutSec)
	}
	if cfg.MaxToolLoops != 44 {
		t.Fatalf("max tool loops: got %d", cfg.MaxToolLoops)
	}
	if cfg.MaxCostUSD == nil || *cfg.MaxCostUSD != 12.5 {
		t.Fatalf("cost: got %v", cfg.MaxCostUSD)
	}
}

func TestResolveLocalRunConfigDefaultsToNoAgentTimeout(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("max-tool-loops", 0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"SPEC.md"})

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, budgetFlags{})
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.AgentTimeoutSec != 0 {
		t.Fatalf("agent timeout should default to disabled, got %d", cfg.AgentTimeoutSec)
	}
}

func TestResolveLocalRunConfigAllowsExplicitNoAgentTimeout(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("max-tool-loops", 0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"--agent-timeout-sec", "0", "SPEC.md"})

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, budgetFlags{})
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.AgentTimeoutSec != 0 {
		t.Fatalf("agent timeout should be disabled, got %d", cfg.AgentTimeoutSec)
	}
}

func TestResolveLocalRunConfigRejectsNegativeAgentTimeout(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("max-tool-loops", 0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"--agent-timeout-sec", "-1", "SPEC.md"})

	_, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, budgetFlags{AgentTimeoutSec: -1})
	if err == nil {
		t.Fatal("expected negative agent timeout to fail")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveSessionRuntimeConfigUsesExplicitFlags(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("max-rounds", 0, "")
	fs.Int("max-duration-sec", 0, "")
	fs.Int("max-input-tokens", 0, "")
	fs.Int("max-output-tokens", 0, "")
	fs.Int("max-tool-loops", 0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{
		"--model", "openai-codex/gpt-5.5",
		"--thinking", "high",
		"--max-cost-usd", "100",
		"--max-rounds", "9",
		"--max-duration-sec", "3600",
		"--max-input-tokens", "120000",
		"--max-output-tokens", "24000",
		"--max-tool-loops", "55",
		"--agent-timeout-sec", "0",
		"SPEC.md",
	})

	cfg, err := resolveSessionRuntimeConfigFromFlags(fs, "openai-codex/gpt-5.5", "high", 100, budgetFlags{MaxRounds: 9, MaxDurationSec: 3600, MaxInputTokens: 120000, MaxOutputTokens: 24000, MaxToolLoops: 55, AgentTimeoutSec: 0})
	if err != nil {
		t.Fatalf("resolveSessionRuntimeConfigFromFlags: %v", err)
	}
	req := sessionapi.SessionCreateRequest{}
	applySessionRuntimeConfig(&req, cfg)
	if req.Model != "openai-codex/gpt-5.5" || req.Thinking != "high" {
		t.Fatalf("model/thinking: got %q/%q", req.Model, req.Thinking)
	}
	if req.MaxCostUSD == nil || *req.MaxCostUSD != 100 {
		t.Fatalf("max cost: got %v", req.MaxCostUSD)
	}
	if req.MaxRounds == nil || *req.MaxRounds != 9 {
		t.Fatalf("max rounds: got %v", req.MaxRounds)
	}
	if req.MaxDurationSec == nil || *req.MaxDurationSec != 3600 {
		t.Fatalf("max duration: got %v", req.MaxDurationSec)
	}
	if req.MaxInputTokens == nil || *req.MaxInputTokens != 120000 {
		t.Fatalf("max input tokens: got %v", req.MaxInputTokens)
	}
	if req.MaxOutputTokens == nil || *req.MaxOutputTokens != 24000 {
		t.Fatalf("max output tokens: got %v", req.MaxOutputTokens)
	}
	if req.MaxToolLoops == nil || *req.MaxToolLoops != 55 {
		t.Fatalf("max tool loops: got %v", req.MaxToolLoops)
	}
	if req.AgentTimeoutSec == nil || *req.AgentTimeoutSec != 0 {
		t.Fatalf("agent timeout: got %v", req.AgentTimeoutSec)
	}
}

func TestUpdateRequestFromCreateCarriesRuntimeConfig(t *testing.T) {
	specMarkdown := "---\nversion: v0\nname: demo\n---\n# Demo\n"
	maxCost := 8.5
	agentTimeout := 7200
	maxRounds := 10
	maxDuration := 7200
	maxInputTokens := 100000
	maxOutputTokens := 20000
	maxToolLoops := 77
	req := sessionapi.SessionCreateRequest{
		SpecMarkdown:    &specMarkdown,
		Model:           "sail-research/moonshotai/Kimi-K2.6",
		Thinking:        "high",
		MaxCostUSD:      &maxCost,
		MaxRounds:       &maxRounds,
		MaxDurationSec:  &maxDuration,
		MaxInputTokens:  &maxInputTokens,
		MaxOutputTokens: &maxOutputTokens,
		MaxToolLoops:    &maxToolLoops,
		AgentTimeoutSec: &agentTimeout,
	}

	update := updateRequestFromCreate(req)
	if update.SpecMarkdown != specMarkdown {
		t.Fatalf("spec markdown: got %q", update.SpecMarkdown)
	}
	if update.Model != req.Model || update.Thinking != req.Thinking {
		t.Fatalf("runtime config: got %#v", update)
	}
	if update.MaxCostUSD == nil || *update.MaxCostUSD != maxCost {
		t.Fatalf("max cost: got %#v", update.MaxCostUSD)
	}
	if update.MaxRounds == nil || *update.MaxRounds != maxRounds {
		t.Fatalf("max rounds: got %#v", update.MaxRounds)
	}
	if update.MaxDurationSec == nil || *update.MaxDurationSec != maxDuration {
		t.Fatalf("max duration: got %#v", update.MaxDurationSec)
	}
	if update.MaxInputTokens == nil || *update.MaxInputTokens != maxInputTokens {
		t.Fatalf("max input tokens: got %#v", update.MaxInputTokens)
	}
	if update.MaxOutputTokens == nil || *update.MaxOutputTokens != maxOutputTokens {
		t.Fatalf("max output tokens: got %#v", update.MaxOutputTokens)
	}
	if update.MaxToolLoops == nil || *update.MaxToolLoops != maxToolLoops {
		t.Fatalf("max tool loops: got %#v", update.MaxToolLoops)
	}
	if update.AgentTimeoutSec == nil || *update.AgentTimeoutSec != agentTimeout {
		t.Fatalf("agent timeout: got %#v", update.AgentTimeoutSec)
	}
}

func TestResolveSessionRuntimeConfigOmitsDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("max-tool-loops", 0, "")
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"SPEC.md"})

	cfg, err := resolveSessionRuntimeConfigFromFlags(fs, "", "medium", 20.0, budgetFlags{})
	if err != nil {
		t.Fatalf("resolveSessionRuntimeConfigFromFlags: %v", err)
	}
	req := sessionapi.SessionCreateRequest{}
	applySessionRuntimeConfig(&req, cfg)
	if req.Model != "" || req.Thinking != "" || req.MaxCostUSD != nil || req.AgentTimeoutSec != nil {
		t.Fatalf("expected empty runtime request config, got %#v", req)
	}
}

func TestRejectCloudApplyRuntimeFlags(t *testing.T) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	fs.Int("max-rounds", 0, "")
	fs.Int("max-duration-sec", 0, "")
	fs.Int("max-input-tokens", 0, "")
	fs.Int("max-output-tokens", 0, "")
	fs.Int("max-tool-loops", 0, "")
	fs.Int("agent-timeout-sec", 0, "")
	fs.Int("autocompact-context-window", 0, "")
	fs.Float64("autocompact-trigger-ratio", 0, "")
	fs.Int("autocompact-keep-recent-tokens", 0, "")
	parseFlags(fs, []string{"--model", "openai-codex/gpt-5.5", "--autocompact-context-window", "64000", "SPEC.md"})

	err := rejectCloudApplyRuntimeFlags(fs)
	if err == nil {
		t.Fatal("expected explicit runtime flags to be rejected")
	}
	if !strings.Contains(err.Error(), "--model") || !strings.Contains(err.Error(), "--autocompact-context-window") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRejectCloudApplyRuntimeFlagsAllowsApplyControls(t *testing.T) {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.String("env", "", "")
	fs.Bool("json", false, "")
	fs.Bool("no-wait", false, "")
	fs.Int("ready-timeout", 900, "")
	parseFlags(fs, []string{"--env", "env_123", "--json", "--no-wait", "--ready-timeout", "30", "SPEC.md"})

	if err := rejectCloudApplyRuntimeFlags(fs); err != nil {
		t.Fatalf("apply controls should be allowed: %v", err)
	}
}

func TestUntilFlagValue(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Int("until", 0, "")
	parseFlags(fs, []string{"--until", "5", "SPEC.md"})

	got, err := untilFlagValue(fs, 5)
	if err != nil {
		t.Fatalf("untilFlagValue: %v", err)
	}
	if got != 5 {
		t.Fatalf("until: got %d", got)
	}
}

func TestUntilFlagValueRejectsNonPositive(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Int("until", 0, "")
	parseFlags(fs, []string{"--until", "0", "SPEC.md"})

	_, err := untilFlagValue(fs, 0)
	if err == nil {
		t.Fatal("expected --until 0 to fail")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveLocalRunConfigRejectsInvalidEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Int("agent-timeout-sec", 0, "")
	parseFlags(fs, []string{"SPEC.md"})
	t.Setenv("TELOS_AGENT_TIMEOUT_SEC", "not-an-int")

	_, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0, budgetFlags{})
	if err == nil {
		t.Fatal("expected invalid environment value to fail")
	}
	if !strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecideLaunchModeMatchesPythonParity(t *testing.T) {
	tests := []struct {
		name            string
		platform        string
		envID           string
		cloudConfigured bool
		localConfigSet  bool
		want            launchMode
		wantErr         string
	}{
		{
			name:     "local spec runs locally",
			platform: "local",
			want:     launchLocal,
		},
		{
			name:           "local spec accepts local flags",
			platform:       "local",
			localConfigSet: true,
			want:           launchLocal,
		},
		{
			name:     "local spec rejects env",
			platform: "local",
			envID:    "env_123",
			wantErr:  "--env cannot be used with platform: local specs",
		},
		{
			name:            "unspecified platform is cloud",
			cloudConfigured: true,
			want:            launchCloudNew,
		},
		{
			name:    "unspecified platform requires cloud login",
			wantErr: "non-local spec requires cloud config",
		},
		{
			name:     "cloud spec with env uses existing env",
			platform: "cloud",
			envID:    "env_123",
			want:     launchCloudExisting,
		},
		{
			name:           "cloud rejects local flags",
			platform:       "cloud",
			localConfigSet: true,
			wantErr:        "local run config flags require a platform: local spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decideLaunchMode(
				tt.platform,
				tt.envID,
				tt.cloudConfigured,
				tt.localConfigSet,
			)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error: got %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("decideLaunchMode: %v", err)
			}
			if got != tt.want {
				t.Fatalf("mode: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionCreateRequestForLocalSpec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SPEC.md"), []byte("---\nname: demo\n---\n# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	req, err := sessionCreateRequestForSpec(dir)
	if err != nil {
		t.Fatalf("sessionCreateRequestForSpec: %v", err)
	}
	if req.SpecMarkdown == nil || !strings.Contains(*req.SpecMarkdown, "name: demo") {
		t.Fatalf("expected spec markdown, got %#v", req)
	}
}

func TestSessionKindForCommand(t *testing.T) {
	if got := sessionKindForCommand("apply"); got != sessionapi.KindController {
		t.Fatalf("apply kind: got %q", got)
	}
	if got := sessionKindForCommand("run"); got != sessionapi.KindTask {
		t.Fatalf("run kind: got %q", got)
	}
}

func TestValidateLaunchCommandRejectsCloudRunOutsideRoot(t *testing.T) {
	for _, mode := range []launchMode{launchCloudExisting, launchCloudNew} {
		err := validateLaunchCommand("run", mode)
		if err == nil {
			t.Fatalf("expected cloud run rejection for %s", mode)
		}
		if !strings.Contains(err.Error(), "inside a root session") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if err := validateLaunchCommand("apply", launchCloudExisting); err != nil {
		t.Fatalf("apply should be allowed: %v", err)
	}
	if err := validateLaunchCommand("run", launchLocal); err != nil {
		t.Fatalf("local run should be allowed: %v", err)
	}
}

func TestSessionCreateRequestRejectsCatalogueSpecID(t *testing.T) {
	_, err := sessionCreateRequestForSpec("cal-diy")
	if err == nil {
		t.Fatal("expected catalogue spec id to fail")
	}
	if !strings.Contains(err.Error(), "spec file not found: cal-diy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionCreateRequestRejectsMissingSpecPath(t *testing.T) {
	for _, input := range []string{"missing/SPEC.md", "../SPEC.md", "SPEC.md"} {
		t.Run(input, func(t *testing.T) {
			_, err := sessionCreateRequestForSpec(input)
			if err == nil {
				t.Fatal("expected missing local spec path to fail")
			}
			if !strings.Contains(err.Error(), "spec file not found") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPackageSpecBuildsApplyPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SPEC.md"), []byte("---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := packageSpec(dir)
	if err != nil {
		t.Fatalf("packageSpec: %v", err)
	}
	if pkg.name != "postgres" {
		t.Fatalf("name: got %q", pkg.name)
	}
	if !strings.HasPrefix(pkg.digest, "sha256:") {
		t.Fatalf("digest: got %q", pkg.digest)
	}
	if len(pkg.bytes) == 0 {
		t.Fatal("missing package bytes")
	}
}

func TestActiveChildSessionsFiltersRunningChildren(t *testing.T) {
	parent := "sess_controller"
	otherParent := "sess_other"
	sessions := []sessionapi.Session{
		{SessionID: "sess_pending", ParentSessionID: &parent, Status: sessionapi.StatusPending},
		{SessionID: "sess_running", ParentSessionID: &parent, Status: sessionapi.StatusRunning},
		{SessionID: "sess_completed", ParentSessionID: &parent, Status: sessionapi.StatusCompleted},
		{SessionID: "sess_other", ParentSessionID: &otherParent, Status: sessionapi.StatusRunning},
	}

	active := activeChildSessions(sessions, parent)
	if len(active) != 2 {
		t.Fatalf("active children: got %#v", active)
	}
	if active[0].SessionID != "sess_pending" || active[1].SessionID != "sess_running" {
		t.Fatalf("active children: got %#v", active)
	}
}

func TestEnsureNoActiveControllerChildAllowsExplicitOverride(t *testing.T) {
	parent := "sess_controller"
	sessions := []sessionapi.Session{
		{SessionID: "sess_running", ParentSessionID: &parent, Status: sessionapi.StatusRunning},
	}

	if err := ensureNoActiveControllerChild(parent, sessions); err == nil {
		t.Fatal("expected active child guard to fail")
	}
	t.Setenv("TELOS_ALLOW_PARALLEL_CHILDREN", "1")
	if err := ensureNoActiveControllerChild(parent, sessions); err == nil {
		t.Fatal("expected override without justification to fail")
	}
	t.Setenv("TELOS_PARALLEL_CHILDREN_JUSTIFICATION", "compare two independent storage backends")
	if err := ensureNoActiveControllerChild(parent, sessions); err != nil {
		t.Fatalf("override should allow active child: %v", err)
	}
}

func TestCloudSessionClientsExplicitUnknownEnvReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/environments" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"environments": []map[string]any{}})
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)

	clients, err := cloudSessionClients("env_missing")
	if err == nil {
		t.Fatal("expected explicit env lookup to return an error")
	}
	if len(clients) != 0 {
		t.Fatalf("expected no clients, got %d", len(clients))
	}
	if !strings.Contains(err.Error(), "environment env_missing not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloudSessionClientsRecoverEnvironmentAccess(t *testing.T) {
	var recovered bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/environments":
			json.NewEncoder(w).Encode(map[string]any{
				"environments": []map[string]any{{
					"id":                         "env_123",
					"env_handle":                 "env-abc.usetelos.ai",
					"state":                      "ready",
					"has_recoverable_env_access": true,
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/environments/env_123/access":
			recovered = true
			json.NewEncoder(w).Encode(map[string]any{
				"id":           "env_123",
				"env_handle":   "env-abc.usetelos.ai",
				"access_token": "env-token",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)

	clients, err := cloudSessionClients("")
	if err != nil {
		t.Fatalf("cloudSessionClients: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("expected one cloud client, got %d", len(clients))
	}
	if !recovered {
		t.Fatal("expected recoverable environment access to be issued")
	}
	access, ok := config.EnvironmentAccessByID("env_123")
	if !ok {
		t.Fatal("expected recovered access to be saved")
	}
	if access.Token != "env-token" {
		t.Fatalf("saved token: got %q", access.Token)
	}
}

func TestGetSessionFromAnywhereReportsCloudClientError(t *testing.T) {
	t.Setenv("TELOS_SESSION_DIR", t.TempDir())
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/environments":
			json.NewEncoder(w).Encode(map[string]any{
				"environments": []map[string]any{{
					"id":                         "env_123",
					"env_handle":                 strings.TrimPrefix(srv.URL, "http://"),
					"state":                      "ready",
					"has_recoverable_env_access": false,
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/sess_remote":
			http.Error(w, "denied", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)
	if err := config.SaveEnvironmentAccessEntry(config.EnvironmentAccess{ID: "env_123", Token: "env-token"}); err != nil {
		t.Fatal(err)
	}

	_, err := getSessionFromAnywhere("sess_remote", "env_123")
	if err == nil || !strings.Contains(err.Error(), "cloud lookup failed") {
		t.Fatalf("expected cloud lookup error, got %v", err)
	}
}

func TestGetSessionFromCloudClientsReportsAllFailures(t *testing.T) {
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer unauthorized.Close()
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer missing.Close()

	_, err := getSessionFromCloudClients("sess_remote", []*cloud.Client{
		cloud.NewClient(unauthorized.URL, "env-token-1"),
		cloud.NewClient(missing.URL, "env-token-2"),
	})
	if err == nil {
		t.Fatal("expected cloud client errors")
	}
	if !strings.Contains(err.Error(), "denied") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected both cloud errors, got %v", err)
	}
}

func TestCloudEnvironmentForApplyDoesNotRequireLocalAccess(t *testing.T) {
	var accessRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/environments":
			json.NewEncoder(w).Encode(map[string]any{
				"environments": []map[string]any{{
					"id":                         "env_123",
					"env_handle":                 "env-abc.usetelos.ai",
					"state":                      "ready",
					"has_recoverable_env_access": false,
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/environments/env_123/access":
			accessRequests++
			http.Error(w, "access should not be requested", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)

	_, env, err := cloudEnvironmentForApply("env_123", false, 0)
	if err != nil {
		t.Fatalf("cloudEnvironmentForApply: %v", err)
	}
	if env.ID != "env_123" || env.Handle != "env-abc.usetelos.ai" {
		t.Fatalf("environment: got %+v", env)
	}
	if accessRequests != 0 {
		t.Fatalf("access requests: got %d", accessRequests)
	}
}

func TestRootSessionContextUsesScopedToken(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	t.Setenv("TELOS_API_TOKEN", "session-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", "http://telos-api.local:8000")

	ctx, ok := rootSessionContext()
	if !ok {
		t.Fatal("expected root context")
	}
	if ctx.endpoint != "http://telos-api.local:8000" {
		t.Fatalf("endpoint: got %q", ctx.endpoint)
	}
	if ctx.token != "session-token" {
		t.Fatalf("token: got %q", ctx.token)
	}
	if ctx.sessionID != "sess_parent" {
		t.Fatalf("session id: got %q", ctx.sessionID)
	}
}

func TestRootSessionContextIgnoresLocalRuntime(t *testing.T) {
	t.Setenv("TELOS_API_TOKEN", "session-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", "http://telos-api.local:8000")

	if ctx, ok := rootSessionContext(); ok {
		t.Fatalf("local runtime should not be cloud root context: %#v", ctx)
	}
}

func TestLocalRootSessionIDUsesLocalSessionContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: local-root\nplatform: local\n---\n# Local Root\n"
	kind := sessionapi.KindController
	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		SessionKind:  &kind,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))

	sessionID, ok := localRootSessionID()
	if !ok {
		t.Fatal("expected local root session context")
	}
	if sessionID != session.SessionID {
		t.Fatalf("session id: got %q", sessionID)
	}
}

func TestLocalRootSessionIDIgnoresTaskSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: local-task\nplatform: local\n---\n# Local Task\n"
	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))

	if sessionID, ok := localRootSessionID(); ok {
		t.Fatalf("task session should not be local root context: %s", sessionID)
	}
}

func TestLocalRootSessionIDRequiresLocalRuntimeMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: local-root\nplatform: local\n---\n# Local Root\n"
	kind := sessionapi.KindController
	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		SessionKind:  &kind,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_RUNTIME", "")
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)

	if sessionID, ok := localRootSessionID(); ok {
		t.Fatalf("session should not be local root context without runtime marker: %s", sessionID)
	}
}

func TestFollowTranscriptWaitsForTranscript(t *testing.T) {
	isolateLocalOnlyConfig(t)
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: follow-test\nplatform: local\n---\n# Follow\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var out bytes.Buffer
	slept := false
	err = followTranscript(session.SessionID, "", &out, func(time.Duration) {
		if slept {
			t.Fatal("unexpected second sleep")
		}
		slept = true
		path := session.Specs[0].TranscriptPath
		if path == nil || *path == "" {
			t.Fatal("missing transcript path")
		}
		if err := os.MkdirAll(filepath.Dir(*path), 0o755); err != nil {
			t.Fatalf("mkdir transcript dir: %v", err)
		}
		if err := os.WriteFile(*path, []byte("# Transcript\n<progress_update>ready</progress_update>\n"), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		if _, err := store.Stop(session.SessionID); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}, false)
	if err != nil {
		t.Fatalf("followTranscript: %v", err)
	}
	if !slept {
		t.Fatal("expected follow to wait for transcript creation")
	}
	if got := out.String(); !strings.Contains(got, "ready") {
		t.Fatalf("output: got %q", got)
	}
}

func TestFollowTranscriptErrorsWhenTerminalWithoutTranscript(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: missing-transcript\nplatform: local\n---\n# Missing\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	var out bytes.Buffer
	err = followTranscript(session.SessionID, "", &out, func(time.Duration) {
		t.Fatal("terminal session should not sleep")
	}, false)
	if err == nil {
		t.Fatal("expected missing terminal transcript to fail")
	}
	if !strings.Contains(err.Error(), "transcript") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetTranscriptFromAnywherePrefersLocalMissingTranscript(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: missing-transcript\nplatform: local\n---\n# Missing\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var sawCloudTranscript bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/environments":
			json.NewEncoder(w).Encode(map[string]any{
				"environments": []map[string]any{{
					"id":                         "env_123",
					"env_handle":                 strings.TrimPrefix(srv.URL, "http://"),
					"state":                      "ready",
					"has_recoverable_env_access": false,
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/"+session.SessionID+"/transcript":
			sawCloudTranscript = true
			http.Error(w, "denied", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)
	if err := config.SaveEnvironmentAccessEntry(config.EnvironmentAccess{ID: "env_123", Token: "env-token"}); err != nil {
		t.Fatal(err)
	}

	_, err = getTranscriptFromAnywhere(session.SessionID, "")
	if err == nil || !strings.Contains(err.Error(), "transcript for session "+session.SessionID) {
		t.Fatalf("expected local missing transcript error, got %v", err)
	}
	if strings.Contains(err.Error(), "cloud lookup failed") {
		t.Fatalf("local missing transcript should not be hidden by cloud error: %v", err)
	}
	if sawCloudTranscript {
		t.Fatal("implicit cloud transcript lookup should not run for an existing local session")
	}
}

func TestGetTranscriptFromAnywherePreservesLocalReadError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: unreadable-transcript\nplatform: local\n---\n# Unreadable\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	transcriptPath := session.Specs[0].TranscriptPath
	if transcriptPath == nil || *transcriptPath == "" {
		t.Fatal("missing transcript path")
	}
	if err := os.MkdirAll(*transcriptPath, 0o755); err != nil {
		t.Fatalf("create transcript path directory: %v", err)
	}

	_, err = getTranscriptFromAnywhere(session.SessionID, "")
	if err == nil {
		t.Fatal("expected local transcript read error")
	}
	if strings.Contains(err.Error(), "transcript for session "+session.SessionID) {
		t.Fatalf("read error should not be rewritten as missing transcript: %v", err)
	}
	if strings.Contains(err.Error(), "cloud lookup failed") {
		t.Fatalf("read error should not fall back to cloud lookup: %v", err)
	}
}

func TestFollowTranscriptSurfacesRootTranscriptError(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/sess_running/transcript":
			http.Error(w, `{"detail":"transcript backend failed"}`, http.StatusInternalServerError)
		case "/api/sessions/sess_running":
			json.NewEncoder(w).Encode(map[string]any{
				"session_id": "sess_running",
				"runtime":    "cloud",
				"status":     "running",
				"config":     map[string]any{},
				"provenance": map[string]any{},
				"specs":      []any{},
				"epochs":     []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cluster.Close()
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_API_TOKEN", "scoped-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", cluster.URL)

	var out bytes.Buffer
	err := followTranscript("sess_running", "", &out, func(time.Duration) {
		t.Fatal("500 transcript errors should not sleep")
	}, false)
	if err == nil {
		t.Fatal("expected transcript error")
	}
	if !strings.Contains(err.Error(), "root transcript lookup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrintLogsDefaultsToProtocolBlocks(t *testing.T) {
	transcript := `# Transcript

	hidden raw content with inline code ` + "`<progress_update>`" + `

<progress_update>First checkpoint</progress_update>

	more raw content with inline code ` + "`</progress_update>`" + `

	<progress_update ts="2026-05-20T00:00:00Z">Second checkpoint</progress_update>

	<review>
criteria,score
Correctness,8.0/10
</review>

	<summary>Needs one more check.</summary>`

	var out bytes.Buffer
	printLogs(&out, transcript, false)
	text := out.String()
	for _, want := range []string{
		"#1 First checkpoint",
		"#2 Second checkpoint",
		"Review\ncriteria,score\nCorrectness,8.0/10",
		"Summary\nNeeds one more check.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "hidden raw content") || strings.Contains(text, "more raw content") {
		t.Fatalf("log output leaked raw transcript:\n%s", text)
	}
}

func TestPrintLogsRawShowsTranscript(t *testing.T) {
	transcript := "# Transcript\nraw content\n<progress_update>Progress</progress_update>\n"

	var out bytes.Buffer
	printLogs(&out, transcript, true)
	if out.String() != transcript {
		t.Fatalf("raw output mismatch:\n%s", out.String())
	}
}

func TestRootLookupReturnsClusterAPIError(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/sess_root" {
			http.Error(w, `{"detail":"cluster unavailable"}`, http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer cluster.Close()
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_API_TOKEN", "scoped-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", cluster.URL)

	_, err := getSessionFromAnywhere("sess_root", "")
	if err == nil {
		t.Fatal("expected root lookup to fail")
	}
	if !strings.Contains(err.Error(), "root session lookup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "session sess_root: not found") {
		t.Fatalf("root error fell through to generic not found: %v", err)
	}
}

func TestRootStopUsesClusterAPI(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	var gotStop bool
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/sessions/sess_child/stop" {
			if r.Header.Get("Authorization") != "Bearer scoped-token" {
				t.Fatalf("authorization: got %q", r.Header.Get("Authorization"))
			}
			gotStop = true
			json.NewEncoder(w).Encode(sessionapi.Session{
				SessionID: "sess_child",
				Status:    sessionapi.StatusStopped,
				Runtime:   sessionapi.RuntimeCloud,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer cluster.Close()
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_API_TOKEN", "scoped-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_CLUSTER_API_ENDPOINT", cluster.URL)

	session, err := stopSessionAnywhere("sess_child", "")
	if err != nil {
		t.Fatalf("stopSessionAnywhere: %v", err)
	}
	if !gotStop {
		t.Fatal("cluster stop endpoint was not called")
	}
	if session.Status != sessionapi.StatusStopped {
		t.Fatalf("status: got %q", session.Status)
	}
}

func TestLocalSessionNotFoundErrorExplainsWorkspaceScope(t *testing.T) {
	isolateLocalOnlyConfig(t)
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))

	_, err := getSessionFromAnywhere("local_missing", "")
	if err == nil {
		t.Fatal("expected missing local session")
	}
	text := err.Error()
	for _, want := range []string{
		"session local_missing not found in",
		"Local sessions are workspace-scoped",
		"TELOS_SESSION_DIR=/path/to/.telos/sessions",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing guidance %q:\n%s", want, text)
		}
	}
}

func TestLocalSessionRootDefaultsToOutputRoot(t *testing.T) {
	t.Setenv("TELOS_SESSION_DIR", "")
	outputRoot := filepath.Join(t.TempDir(), "telos-output")
	t.Setenv("TELOS_OUTPUT_ROOT", outputRoot)

	got := localSessionRoot()
	prefix := outputRoot + string(os.PathSeparator) + "execroot" + string(os.PathSeparator)
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("local session root %q should be under %q", got, prefix)
	}
	if !strings.HasSuffix(got, string(os.PathSeparator)+"sessions") {
		t.Fatalf("local session root %q should end with sessions", got)
	}
}

func TestLocalSessionRootHonorsSessionDirEnv(t *testing.T) {
	want := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("TELOS_SESSION_DIR", want)
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(t.TempDir(), "telos-output"))

	got := localSessionRoot()
	if got != want {
		t.Fatalf("local session root: got %q want %q", got, want)
	}
}

func configureCloudTest(t *testing.T, endpoint string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))
	t.Setenv("TELOS_ENVIRONMENTS_CONFIG", filepath.Join(dir, "environments.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", endpoint)
	t.Setenv("TELOS_AUTH_TOKEN", "control-token")
}

func isolateLocalOnlyConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))
	t.Setenv("TELOS_ENVIRONMENTS_CONFIG", filepath.Join(dir, "environments.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")
}
