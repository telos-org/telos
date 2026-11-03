package game

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	finalStatusRE        = regexp.MustCompile(`(?:^|\n)\s*<status>\w+</status>\s*$`)
	liveProgressUpdateRE = regexp.MustCompile(`(?si)<progress_update\b[^>]*>\s*(.*?)\s*</progress_update>`)
)

const maxTurnBodyChars = 8000

type AppendTurnOptions struct {
	IncludeStatus    bool
	RunnerLogPath    string
	EpochID          int
	RecordedProgress map[string]bool
}

type ExternalUpdate struct {
	Message             string
	PreviousSpecVersion int
	CurrentSpecVersion  int
	SpecPath            string
	PreviousSpecPath    string
	CurrentSpecPath     string
	ActiveSpecPath      string
	DiffPath            string
	PreviousRevision    string
	CurrentRevision     string
}

// InitializeTranscript creates a transcript header if the file does not exist.
func InitializeTranscript(path, sessionID, systemName, evidencePath, startedAt string) error {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		if info.Size() > 0 {
			return nil
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("inspect transcript %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`# Session Transcript: %s

- System: `+"`%s`"+`
- Started: `+"`%s`"+`
- Evidence: `+"`%s`"+`

This append-only transcript is the session communication log.
It is also the background-agent progress surface.
The live system remains the source of truth.
`, sessionID, systemName, startedAt, evidencePath)
	return os.WriteFile(path, []byte(content), 0o644)
}

// ReadTranscript reads the transcript file content.
func ReadTranscript(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// AppendTurn appends one implementation or evaluation turn to the transcript.
func AppendTurn(path string, role string, roleRound int, status string, logs string, stats *TurnStats, turnID string, turnError string) error {
	return AppendTurnWithOptions(path, role, roleRound, status, logs, stats, turnID, turnError,
		AppendTurnOptions{IncludeStatus: true})
}

// AppendTurnWithOptions appends one implementation or evaluation turn to the transcript.
func AppendTurnWithOptions(path string, role string, roleRound int, status string, logs string, stats *TurnStats, turnID string, turnError string, opts AppendTurnOptions) error {
	label := "Implementation"
	if role == "verifier" {
		label = "Evaluation"
	}
	body := "_Turn failed; no completed agent report._"
	if turnError == "" {
		body = turnBody(logs, opts.RecordedProgress)
	} else if opts.RunnerLogPath != "" {
		body += fmt.Sprintf("\n\nDetails: `%s` (epoch `%d`, turn `%s`).", opts.RunnerLogPath, opts.EpochID, turnID)
	} else {
		body += " Diagnostic log unavailable."
	}
	if turnError == "" && opts.IncludeStatus {
		body = strings.TrimSpace(fmt.Sprintf("%s\n\n<status>%s</status>", body, status))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "\n## %s %d\n\n", label, roleRound)
	meta := turnMeta(stats, turnID)
	if meta != "" {
		fmt.Fprintf(f, "%s\n\n", meta)
	}
	fmt.Fprintf(f, "%s\n", body)
	return nil
}

// ExtractLiveAgentEvents returns progress updates to surface mid-turn.
func ExtractLiveAgentEvents(text string) []LiveAgentEvent {
	var events []LiveAgentEvent
	for _, match := range liveProgressUpdateRE.FindAllStringSubmatch(text, -1) {
		body := strings.TrimSpace(match[1])
		if body == "" {
			continue
		}
		events = append(events, LiveAgentEvent{Kind: "progress_update", Text: body})
	}
	return events
}

// AppendLiveAgentEvent appends a mid-turn progress artifact to the transcript.
func AppendLiveAgentEvent(path string, role string, roleRound int, turnID string, event LiveAgentEvent) error {
	if event.Kind == "" || strings.TrimSpace(event.Text) == "" {
		return nil
	}
	label := "Implementation"
	if role == "verifier" {
		label = "Evaluation"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	meta := turnMeta(nil, turnID)
	if meta != "" {
		meta += "\n\n"
	}
	_, err = fmt.Fprintf(f, "\n## Live %s %d\n\n%s<%s>%s</%s>\n", label, roleRound, meta, event.Kind, strings.TrimSpace(event.Text), event.Kind)
	return err
}

func AppendExternalUpdate(path string, update ExternalUpdate) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	message := strings.TrimSpace(update.Message)
	if message == "" {
		message = "The operator updated the session spec. Reload the current spec before continuing."
	}
	fmt.Fprint(f, "\n## External Update\n\n")
	fmt.Fprintln(f, "<external_update>")
	fmt.Fprintln(f, message)
	fmt.Fprintln(f)
	fmt.Fprintf(f, "- Previous spec version: `%d`\n", update.PreviousSpecVersion)
	fmt.Fprintf(f, "- Current spec version: `%d`\n", update.CurrentSpecVersion)
	if update.PreviousRevision != "" {
		fmt.Fprintf(f, "- Previous revision: `%s`\n", update.PreviousRevision)
	}
	if update.CurrentRevision != "" {
		fmt.Fprintf(f, "- Current revision: `%s`\n", update.CurrentRevision)
	}
	if update.PreviousSpecPath != "" {
		fmt.Fprintf(f, "- Previous spec path: `%s`\n", update.PreviousSpecPath)
	}
	if update.CurrentSpecPath != "" {
		fmt.Fprintf(f, "- Current immutable spec path: `%s`\n", update.CurrentSpecPath)
	}
	if update.ActiveSpecPath != "" {
		fmt.Fprintf(f, "- Active spec path: `%s`\n", update.ActiveSpecPath)
	}
	if update.DiffPath != "" {
		fmt.Fprintf(f, "- Spec diff path: `%s`\n", update.DiffPath)
	}
	if update.SpecPath != "" && update.SpecPath != update.ActiveSpecPath && update.SpecPath != update.CurrentSpecPath {
		fmt.Fprintf(f, "- Current spec path: `%s`\n", update.SpecPath)
	}
	fmt.Fprintln(f, "</external_update>")
	return nil
}

// AppendGameResult appends the terminal session result to the transcript.
func AppendGameResult(path string, result string, runnerLogPath string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "\n## Result\n\n- Status: `%s`\n", result)
	if runnerLogPath != "" {
		fmt.Fprintf(f, "- Runner log: `%s`\n", runnerLogPath)
	}
	return nil
}

func turnMeta(stats *TurnStats, turnID string) string {
	var parts []string
	if turnID != "" {
		parts = append(parts, fmt.Sprintf("turn `%s`", turnID))
	}
	if stats != nil {
		if stats.Model != "" {
			parts = append(parts, fmt.Sprintf("model `%s`", stats.Model))
		}
		if stats.CostUSD > 0 {
			parts = append(parts, fmt.Sprintf("cost `$%.4f`", stats.CostUSD))
		}
		if stats.NumTurns > 0 {
			parts = append(parts, fmt.Sprintf("tool turns `%d`", stats.NumTurns))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("_Turn metadata: %s._", strings.Join(parts, ", "))
}

func turnBody(logs string, recordedProgress map[string]bool) string {
	stripped := stripFinalStatus(strings.TrimSpace(logs))
	if stripped == "" {
		return "_No assistant text captured._"
	}
	stripped = liveProgressUpdateRE.ReplaceAllStringFunc(stripped, func(block string) string {
		text := strings.TrimSpace(liveProgressUpdateRE.FindStringSubmatch(block)[1])
		if recordedProgress[text] {
			return ""
		}
		return block
	})
	return capTurnBody(strings.TrimSpace(stripped))
}

func stripFinalStatus(body string) string {
	return strings.TrimRight(finalStatusRE.ReplaceAllString(body, ""), " \t\n\r")
}

func capTurnBody(body string) string {
	if len(body) <= maxTurnBodyChars {
		return body
	}
	trimmed := strings.TrimSpace(body[len(body)-maxTurnBodyChars:])
	lines := []string{
		fmt.Sprintf("_Transcript turn truncated to the last %d chars._", maxTurnBodyChars),
	}
	lines = append(lines, "", trimmed)
	return strings.Join(lines, "\n")
}
