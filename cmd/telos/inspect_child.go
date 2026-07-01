package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func cmdInspectChild(args []string) {
	fs := flag.NewFlagSet("inspect-child", flag.ExitOnError)
	env := fs.String("env", "", "Cloud environment")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos inspect-child CHILD_SESSION [--env ENV] [--json]")
		os.Exit(1)
	}
	report, err := inspectChildSession(fs.Arg(0), *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(report)
		return
	}
	printChildInspection(os.Stdout, report)
}

type childInspectionReport struct {
	ChildSessionID      string          `json:"child_session_id"`
	ParentSessionID     string          `json:"parent_session_id,omitempty"`
	Status              string          `json:"status"`
	Result              string          `json:"result,omitempty"`
	Completion          string          `json:"completion,omitempty"`
	Terminal            bool            `json:"terminal"`
	WorkspacePath       string          `json:"workspace_path,omitempty"`
	WorkspaceExists     bool            `json:"workspace_exists"`
	TranscriptPath      string          `json:"transcript_path,omitempty"`
	EvidencePath        string          `json:"evidence_path,omitempty"`
	ObjectiveLedgerPath string          `json:"objective_ledger_path,omitempty"`
	Analysis            sessionAnalysis `json:"analysis"`
	ReadyToReconcile    bool            `json:"ready_to_reconcile"`
	Checklist           []string        `json:"checklist"`
	InspectionPath      string          `json:"inspection_path,omitempty"`
	InspectedAt         string          `json:"inspected_at"`
}

func inspectChildSession(sessionID, envID string) (childInspectionReport, error) {
	session, events, err := getSessionAnalysisInput(sessionID, envID)
	if err != nil {
		return childInspectionReport{}, err
	}
	return buildChildInspection(session, events)
}

func buildChildInspection(session *sessionapi.Session, events []sessionapi.SessionEvent) (childInspectionReport, error) {
	if session == nil {
		return childInspectionReport{}, fmt.Errorf("child session missing")
	}
	if session.ParentSessionID == nil || strings.TrimSpace(*session.ParentSessionID) == "" {
		return childInspectionReport{}, fmt.Errorf("session %s is not a child session", session.SessionID)
	}
	analysis := analyzeSessionEvents(session, events)
	report := childInspectionReport{
		ChildSessionID:  session.SessionID,
		ParentSessionID: *session.ParentSessionID,
		Status:          string(session.Status),
		Result:          ptrString(session.Result),
		Completion:      ptrString(session.CompletionReason),
		Terminal:        session.Status.IsTerminal(),
		Analysis:        analysis,
		InspectedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if len(session.Specs) > 0 {
		spec := session.Specs[0]
		report.WorkspacePath = ptrString(spec.WorkspacePath)
		report.WorkspaceExists = boolValue(spec.WorkspaceExists)
		report.TranscriptPath = ptrString(spec.TranscriptPath)
		report.EvidencePath = ptrString(spec.EvidencePath)
		report.ObjectiveLedgerPath = ptrString(spec.ObjectiveLedgerPath)
	}
	report.Checklist = childInspectionChecklist(report)
	report.ReadyToReconcile = childReadyToReconcile(report)
	if session.SessionDir != nil && *session.SessionDir != "" {
		if path, err := writeChildInspectionMarker(*session.SessionDir, report); err == nil {
			report.InspectionPath = path
		} else {
			return report, err
		}
	}
	return report, nil
}

func childInspectionChecklist(report childInspectionReport) []string {
	var items []string
	if report.Terminal {
		items = append(items, "terminal child status inspected")
	} else {
		items = append(items, "child is still active; wait or stop before reconciliation")
	}
	if report.WorkspaceExists {
		items = append(items, "workspace checkpoint exists")
	} else {
		items = append(items, "workspace checkpoint missing")
	}
	if report.TranscriptPath != "" {
		items = append(items, "transcript path recorded")
	}
	if report.EvidencePath != "" {
		items = append(items, "evidence path recorded")
	}
	if len(report.Analysis.Failures) == 0 {
		items = append(items, "analysis found no blocking failure taxonomy entries")
	} else {
		items = append(items, "analysis failures must be reconciled before parent acceptance")
	}
	items = append(items, "compare child workspace against parent live artifact before merging")
	items = append(items, "preserve reusable tests, probes, fixtures, and verification evidence")
	return items
}

func childReadyToReconcile(report childInspectionReport) bool {
	if !report.Terminal || !report.WorkspaceExists {
		return false
	}
	if report.Status != string(sessionapi.StatusCompleted) {
		return false
	}
	result := strings.ToLower(strings.TrimSpace(report.Result))
	if result != "completed" && result != "success" {
		return false
	}
	return len(report.Analysis.Failures) == 0
}

func writeChildInspectionMarker(childSessionDir string, report childInspectionReport) (string, error) {
	parentDir := childSessionDir
	if safeSessionIDPathSegment(report.ParentSessionID) {
		parentDir = filepath.Join(filepath.Dir(childSessionDir), report.ParentSessionID)
	}
	if info, err := os.Stat(parentDir); err != nil || !info.IsDir() {
		parentDir = childSessionDir
	}
	dir := filepath.Join(parentDir, "child-inspections")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if !safeSessionIDPathSegment(report.ChildSessionID) {
		return "", fmt.Errorf("invalid child session id %q", report.ChildSessionID)
	}
	path := filepath.Join(dir, report.ChildSessionID+".json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func safeSessionIDPathSegment(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." {
		return false
	}
	return filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}

func printChildInspection(out io.Writer, report childInspectionReport) {
	fmt.Fprintln(out, "Child Inspection")
	printDetailField(out, "child", report.ChildSessionID)
	printDetailField(out, "parent", report.ParentSessionID)
	printDetailField(out, "status", report.Status)
	printDetailField(out, "result", report.Result)
	printDetailField(out, "completion", report.Completion)
	printDetailField(out, "ready", fmt.Sprint(report.ReadyToReconcile))
	printDetailField(out, "workspace", artifactPath(&report.WorkspaceExists, &report.WorkspacePath))
	printDetailField(out, "transcript", fileOrDash(report.TranscriptPath))
	printDetailField(out, "evidence", fileOrDash(report.EvidencePath))
	printDetailField(out, "ledger", fileOrDash(report.ObjectiveLedgerPath))
	if report.InspectionPath != "" {
		printDetailField(out, "inspection", fileURI(report.InspectionPath))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Checklist")
	for _, item := range report.Checklist {
		fmt.Fprintf(out, "  - %s\n", item)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Failure Taxonomy")
	printCountTable(out, report.Analysis.Failures)
}

func fileOrDash(path string) string {
	if strings.TrimSpace(path) == "" {
		return "-"
	}
	return fileURI(path)
}

func boolValue(value *bool) bool {
	return value != nil && *value
}
