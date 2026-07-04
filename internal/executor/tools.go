package executor

import (
	"fmt"
	"strings"
)

const (
	defaultToolTimeoutSec      = 120
	defaultToolMaxBytes        = 96 << 10
	defaultToolMaxLines        = 400
	defaultToolReadLines       = 400
	defaultToolSearchLineBytes = 4096
)

// Security model: these tools are deliberately unsandboxed. This is a YOLO /
// minimal, Pi-inspired posture of full trust in the agent and its host: reads,
// writes, and `bash` operate on whatever the process can touch, with no
// workspace jail or write allowlist. The only real containment is whatever
// sandbox the executor itself runs inside (e.g. an ephemeral container/pod).
// Relative paths still resolve against the workspace for convenience, and
// out-of-workspace access is logged for telemetry, but neither is a security
// boundary — `bash` and absolute paths bypass both by design. Do not treat any
// path handling here as a trust boundary.

type nativeToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type nativeToolResult struct {
	CallID      string
	Name        string
	Output      string
	IsError     bool
	ErrorCode   executorErrorCode
	Metadata    map[string]any
	DurationMS  int64
	ExitCode    int
	HasExitCode bool
	Truncated   bool
}

// -- Structured tool output --------------------------------------------------

// toolField is one ordered metadata key-value pair rendered as "key: value".
// The Value is typed (bool, int, string) so it populates Metadata directly
// without re-parsing the rendered text.
type toolField struct {
	Key   string
	Value any
}

// toolBodySection is a labeled freeform block rendered as "key:\ntext". A tool
// may emit several (e.g. bash emits stdout and stderr as separate sections).
type toolBodySection struct {
	Key  string
	Text string
}

// toolOutput is the structured result of a tool handler. execute() builds the
// nativeToolResult (Metadata, ExitCode, Truncated, ErrorCode) directly from
// these fields, eliminating the old applyMetadataFromOutput re-parse.
type toolOutput struct {
	fields    []toolField
	bodies    []toolBodySection
	exitCode  *int
	truncated bool
	errorCode executorErrorCode
}

// innerText renders the fields and body sections without the envelope header
// (tool/ok/duration_ms). Used when one handler embeds another's output.
func (o toolOutput) innerText() string {
	var parts []string
	for _, f := range o.fields {
		parts = append(parts, fmt.Sprintf("%s: %v", f.Key, f.Value))
	}
	for _, b := range o.bodies {
		if b.Key != "" {
			if b.Text != "" {
				parts = append(parts, b.Key+":")
				parts = append(parts, b.Text)
			} else {
				parts = append(parts, b.Key+":")
			}
		} else {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// toolFields builds a []toolField from alternating key/value pairs.
func toolFields(kvs ...any) []toolField {
	fields := make([]toolField, 0, len(kvs)/2)
	for i := 0; i+1 < len(kvs); i += 2 {
		key, _ := kvs[i].(string)
		fields = append(fields, toolField{Key: key, Value: kvs[i+1]})
	}
	return fields
}

// renderToolOutput produces the model-facing text. The format is identical to
// what the handlers used to fmt.Sprintf by hand: envelope header lines, then
// metadata fields, then body sections.
func renderToolOutput(name string, ok bool, durationMS int64, out toolOutput) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("tool: %s", name))
	parts = append(parts, fmt.Sprintf("ok: %t", ok))
	parts = append(parts, fmt.Sprintf("duration_ms: %d", durationMS))
	for _, f := range out.fields {
		parts = append(parts, fmt.Sprintf("%s: %v", f.Key, f.Value))
	}
	for _, b := range out.bodies {
		if b.Key != "" {
			if b.Text != "" {
				parts = append(parts, b.Key+":")
				parts = append(parts, b.Text)
			} else {
				parts = append(parts, b.Key+":")
			}
		} else {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

type nativeSkillRef struct {
	Name        string
	Description string
	Path        string
	Required    bool
}

type toolOutputLimit struct {
	MaxBytes int
	MaxLines int
}

func defaultToolOutputLimit(knobs envKnobs) toolOutputLimit {
	return toolOutputLimit{
		MaxBytes: knobs.ToolMaxBytes,
		MaxLines: knobs.ToolMaxLines,
	}
}

// nativeTool is the single source of truth for a tool: its schema and its
// handler. Schema generation, dispatch, and the system-prompt name list all
// derive from the same table. Only the canonical name is advertised to the
// model; aliases are still accepted on dispatch so models that emit the shorter
// conventional names (read/write/edit/ls/grep/find) keep working.
