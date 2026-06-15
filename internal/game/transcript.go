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
	finalStatusRE         = regexp.MustCompile(`(?:^|\n)\s*<status>\w+</status>\s*$`)
	finalProgressUpdateRE = regexp.MustCompile(`(?s)(?:^|\n)\s*<progress_update>.*?</progress_update>\s*$`)
	progressUpdateRE      = regexp.MustCompile(`(?si)<progress_update>\s*(.*?)\s*</progress_update>`)
)

const maxTurnBodyChars = 8000

type AppendTurnOptions struct {
	IncludeStatus bool
	PiSessionPath string
	EvidencePath  string
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
	body := stripFinalStatus(turnBody(logs, turnError, opts))
	if turnError != "" || !hasFinalProgressUpdate(body) {
		body = fmt.Sprintf("%s\n\n<progress_update>%s</progress_update>",
			body, fallbackProgressUpdate(body, status, turnError, opts.IncludeStatus))
	}
	if opts.IncludeStatus {
		body = fmt.Sprintf("%s\n\n<status>%s</status>", body, status)
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

// AppendGameResult appends the terminal session result to the transcript.
func AppendGameResult(path string, result string, errorMsg string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "\n## Result\n\n- Status: `%s`\n", result)
	if errorMsg != "" {
		fmt.Fprintf(f, "- Error: `%s`\n", errorMsg)
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

func turnBody(logs, turnError string, opts AppendTurnOptions) string {
	stripped := stripFinalStatus(strings.TrimSpace(logs))
	if turnError != "" {
		return runtimeErrorBody(turnError, opts)
	}
	if stripped == "" {
		return "_No assistant text captured._"
	}
	return capTurnBody(stripped)
}

func runtimeErrorBody(err string, opts AppendTurnOptions) string {
	lines := []string{fmt.Sprintf("_Turn ended with runtime error: `%s`._", err)}
	if opts.PiSessionPath != "" || opts.EvidencePath != "" {
		lines = append(lines, "", "Inspect the canonical turn artifacts before judging or continuing:")
		if opts.PiSessionPath != "" {
			lines = append(lines, fmt.Sprintf("- Agent session: `%s`", opts.PiSessionPath))
		}
		if opts.EvidencePath != "" {
			lines = append(lines, fmt.Sprintf("- Evidence log: `%s`", opts.EvidencePath))
		}
	}
	return strings.Join(lines, "\n")
}

func hasFinalProgressUpdate(body string) bool {
	return finalProgressUpdateRE.MatchString(strings.TrimRight(body, " \t\n\r"))
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

func fallbackProgressUpdate(body, status, turnError string, includeStatus bool) string {
	if turnError != "" {
		return fmt.Sprintf("Turn ended with runtime error: %s.", turnError)
	}
	matches := progressUpdateRE.FindAllStringSubmatch(body, -1)
	if len(matches) > 0 {
		return strings.TrimSpace(matches[len(matches)-1][1])
	}
	if !includeStatus {
		return "Turn completed."
	}
	return fmt.Sprintf("Turn completed with status %s.", status)
}
