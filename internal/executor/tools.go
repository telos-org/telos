package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
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
type nativeTool struct {
	name        string
	aliases     []string
	description string
	parameters  map[string]interface{}
	run         func(t *nativeTools, ctx context.Context, args map[string]interface{}) (string, error)
}

// nativeToolDefs is the tool table, built once at package init. The handler
// closures are stateless (they take the per-turn *nativeTools as an argument),
// so a single shared table is safe to reuse across every turn and process.
var nativeToolDefs = buildNativeToolTable()

func buildNativeToolTable() []nativeTool {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	boolean := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "boolean", "description": desc}
	}
	integer := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "integer", "description": desc}
	}
	obj := func(required []string, props map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"type":                 "object",
			"required":             required,
			"properties":           props,
			"additionalProperties": false,
		}
	}
	return []nativeTool{
		{
			name:        "read_file",
			aliases:     []string{"read"},
			description: "Read a bounded UTF-8 file range. Relative paths resolve inside the workspace; absolute paths may be read.",
			parameters:  obj([]string{"path"}, map[string]interface{}{"path": str("Relative workspace path or absolute container path."), "start_line": integer("Optional 1-based start line."), "limit_lines": integer("Optional maximum lines to return.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.readFile(argString(args, "path"), argInt(args, "start_line"), argInt(args, "limit_lines"))
			},
		},
		{
			name:        "write_file",
			aliases:     []string{"write"},
			description: "Create or overwrite a UTF-8 file at any path the process can write.",
			parameters:  obj([]string{"path", "content"}, map[string]interface{}{"path": str("Relative workspace path or absolute path."), "content": str("Complete file content to write.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.write(argString(args, "path"), argString(args, "content"))
			},
		},
		{
			name:        "replace_text",
			aliases:     []string{"edit"},
			description: "Replace text in an existing UTF-8 file with an optional exact expected replacement count.",
			parameters:  obj([]string{"path", "old_string", "new_string"}, map[string]interface{}{"path": str("Relative workspace path or absolute path."), "old_string": str("Exact text to replace."), "new_string": str("Replacement text."), "replace_all": boolean("Replace every occurrence instead of only the first."), "expected_count": integer("Optional required replacement count.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.edit(argString(args, "path"), argString(args, "old_string"), argString(args, "new_string"), argBool(args, "replace_all"), argInt(args, "expected_count"))
			},
		},
		{
			name:        "apply_patch",
			description: "Apply a unified diff patch to the workspace. Prefer this for line-oriented multi-file edits.",
			parameters:  obj([]string{"patch"}, map[string]interface{}{"patch": str("Unified diff patch text.")}),
			run: func(t *nativeTools, ctx context.Context, args map[string]interface{}) (string, error) {
				return t.applyPatch(ctx, argString(args, "patch"))
			},
		},
		{
			name:        "bash",
			description: "Run a shell command in the workspace with bounded output and optional cwd/env.",
			parameters: obj([]string{"command"}, map[string]interface{}{
				"command":         str("Command to run with bash -lc."),
				"timeout_seconds": integer("Optional timeout, capped by Telos."),
				"cwd":             str("Optional working directory relative to the workspace."),
				"env": map[string]interface{}{
					"type":                 "object",
					"description":          "Optional per-command environment variables. Names must match [A-Za-z_][A-Za-z0-9_]*.",
					"additionalProperties": map[string]interface{}{"type": "string"},
				},
			}),
			run: func(t *nativeTools, ctx context.Context, args map[string]interface{}) (string, error) {
				env, err := argEnv(args, "env")
				if err != nil {
					return "", err
				}
				return t.bash(ctx, argString(args, "command"), argString(args, "cwd"), env, argInt(args, "timeout_seconds"))
			},
		},
		{
			name:        "list_dir",
			aliases:     []string{"ls"},
			description: "List a bounded directory. Relative paths resolve inside the workspace; absolute paths may be read.",
			parameters:  obj([]string{}, map[string]interface{}{"path": str("Directory path, defaults to workspace root.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.ls(argString(args, "path"))
			},
		},
		{
			name:        "search_text",
			aliases:     []string{"grep"},
			description: "Search text files with a regular expression and bounded match output.",
			parameters:  obj([]string{"pattern"}, map[string]interface{}{"pattern": str("Go regular expression."), "path": str("Directory or file path, defaults to workspace root."), "max_matches": integer("Maximum matches to return.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.grep(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
			},
		},
		{
			name:        "find_files",
			aliases:     []string{"find"},
			description: "Find files by glob pattern with bounded output. Supports `**` for recursive directory matches (e.g. `**/*.go`, `src/**/foo_*.txt`); a bare pattern like `*.go` matches files at any depth.",
			parameters:  obj([]string{"pattern"}, map[string]interface{}{"pattern": str("Glob pattern matched against relative paths and basenames; supports `**`."), "path": str("Directory path, defaults to workspace root."), "max_matches": integer("Maximum paths to return.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.find(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
			},
		},
		{
			name:        "file_info",
			description: "Return file metadata such as type, byte size, mode, and line count for text files.",
			parameters:  obj([]string{"path"}, map[string]interface{}{"path": str("Relative workspace path or absolute container path.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.fileInfo(argString(args, "path"))
			},
		},
		{
			name:        "skill",
			description: "List available Telos skills, read one skill body, or read a referenced file inside a skill directory. Use this for required rubrics and task-specific skills.",
			parameters: obj([]string{"action"}, map[string]interface{}{
				"action":      str("Either 'list', 'read', or 'read_ref'."),
				"name":        str("Skill name for action='read' or action='read_ref'."),
				"path":        str("Relative file path inside the skill directory for action='read_ref'."),
				"start_line":  integer("Optional 1-based start line for read/read_ref."),
				"limit_lines": integer("Optional maximum lines to return for read/read_ref."),
			}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.skill(argString(args, "action"), argString(args, "name"), argString(args, "path"), argInt(args, "start_line"), argInt(args, "limit_lines"))
			},
		},
	}
}

func nativeToolNames() []string {
	names := make([]string, len(nativeToolDefs))
	for i, def := range nativeToolDefs {
		names[i] = def.name
	}
	return names
}

// nativeToolsForOpenAI renders the tool table as openai-go Responses function
// tools. The LiteLLM proxy is OpenAI-compatible, so this is the only schema
// shape Telos needs to emit.
func nativeToolsForOpenAI() []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(nativeToolDefs))
	for _, def := range nativeToolDefs {
		out = append(out, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        def.name,
				Description: openai.String(def.description),
				Parameters:  def.parameters,
				Strict:      openai.Bool(false),
			},
		})
	}
	return out
}

type nativeTools struct {
	platform      *platform.LocalPlatform
	stopRequested func() bool
	byName        map[string]nativeTool
	limit         toolOutputLimit
	skills        map[string]nativeSkillRef
	// skillCoverage records, per skill name, the number of leading lines read
	// contiguously from line 1. A required rubric counts as fully read once this
	// reaches its line count, so paginated reads (start_line walking to EOF)
	// satisfy the read gate just like a single complete read.
	skillCoverage map[string]int
	openedSkills  map[string]bool
	logger        *nativeSessionLogger
}

func newNativeTools(p *platform.LocalPlatform, stopRequested func() bool, skills []game.TurnSkill, logger *nativeSessionLogger, knobs envKnobs) *nativeTools {
	t := &nativeTools{
		platform:      p,
		stopRequested: stopRequested,
		byName:        map[string]nativeTool{},
		limit:         defaultToolOutputLimit(knobs),
		skills:        skillRefsFromTurn(skills),
		skillCoverage: map[string]int{},
		openedSkills:  map[string]bool{},
		logger:        logger,
	}
	for _, tool := range nativeToolDefs {
		t.byName[tool.name] = tool
		for _, alias := range tool.aliases {
			t.byName[alias] = tool
		}
	}
	return t
}

// skillRefsFromTurn builds the skill lookup from the structured roster the
// runtime passes through TurnState, keyed by skill name.
func skillRefsFromTurn(skills []game.TurnSkill) map[string]nativeSkillRef {
	refs := make(map[string]nativeSkillRef, len(skills))
	for _, s := range skills {
		name := strings.TrimSpace(s.Name)
		if name == "" || strings.TrimSpace(s.SkillPath) == "" {
			continue
		}
		refs[name] = nativeSkillRef{
			Name:        name,
			Description: strings.TrimSpace(s.Description),
			Path:        s.SkillPath,
			Required:    s.Required,
		}
	}
	return refs
}

func (t *nativeTools) executeAll(ctx context.Context, calls []nativeToolCall) []nativeToolResult {
	results := make([]nativeToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, t.execute(ctx, call))
	}
	return results
}

func (t *nativeTools) execute(ctx context.Context, call nativeToolCall) nativeToolResult {
	if call.ID == "" {
		call.ID = call.Name
	}
	args := map[string]interface{}{}
	if strings.TrimSpace(call.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return nativeToolResult{CallID: call.ID, Name: call.Name, Output: "invalid tool arguments: " + err.Error(), IsError: true, ErrorCode: errAgentProtocol}
		}
	}
	tool, ok := t.byName[call.Name]
	if !ok {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: fmt.Sprintf("unknown tool %q; available tools are %s", call.Name, oxfordList(nativeToolNames())), IsError: true, ErrorCode: errAgentProtocol}
	}
	if err := validateToolArgs(tool, args); err != nil {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: err.Error(), IsError: true, ErrorCode: errAgentProtocol}
	}
	started := time.Now()
	output, err := tool.run(t, ctx, args)
	durationMS := time.Since(started).Milliseconds()
	if err != nil {
		result := nativeToolResult{CallID: call.ID, Name: call.Name, Output: formatToolEnvelope(call.Name, false, durationMS, err.Error()), IsError: true, DurationMS: durationMS}
		result.applyMetadataFromOutput()
		return result
	}
	result := nativeToolResult{CallID: call.ID, Name: call.Name, Output: formatToolEnvelope(call.Name, true, durationMS, output), DurationMS: durationMS}
	result.applyMetadataFromOutput()
	return result
}

func (r *nativeToolResult) applyMetadataFromOutput() {
	if r == nil {
		return
	}
	// Only scan the envelope header (the leading `key: value` lines), not the
	// freeform body that follows a section key like `content:`/`stdout:`. The
	// body can be arbitrary file or command output, so scanning it would let a
	// file line such as `exit_code: 7` spoof tool metadata.
	header := toolEnvelopeHeader(r.Output)
	for _, line := range strings.Split(header, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if toolMetadataKey(key) {
			if r.Metadata == nil {
				r.Metadata = map[string]any{}
			}
			r.Metadata[key] = parseToolMetadataValue(value)
		}
		switch key {
		case "exit_code":
			if n, err := strconv.Atoi(value); err == nil {
				r.ExitCode = n
				r.HasExitCode = true
			}
		case "stdout_truncated", "stderr_truncated", "truncated":
			if strings.EqualFold(value, "true") {
				r.Truncated = true
			}
		}
	}
	if r.IsError && r.ErrorCode == "" {
		r.ErrorCode = classifyToolResultError(header)
	}
}

// toolEnvelopeHeader returns the leading metadata lines of a tool envelope,
// stopping at the first body section (`content:`, `stdout:`, ...) whose value
// is freeform multi-line output rather than a metadata field.
func toolEnvelopeHeader(output string) string {
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && toolBodySectionKey(strings.TrimSpace(key)) {
			return strings.Join(lines[:i], "\n")
		}
	}
	return output
}

func toolBodySectionKey(key string) bool {
	switch key {
	case "content", "stdout", "stderr", "entries", "matches", "paths", "files":
		return true
	default:
		return false
	}
}

func toolMetadataKey(key string) bool {
	switch key {
	case "tool", "ok", "exit_code", "signal", "started_at", "ended_at", "duration_ms", "timed_out", "interrupted",
		"stdout_bytes", "stdout_original_bytes", "stdout_original_lines", "stdout_truncated",
		"stderr_bytes", "stderr_original_bytes", "stderr_original_lines", "stderr_truncated",
		"path", "size_bytes", "lines_returned", "line_count", "entry_count", "entries_returned", "match_count",
		"created", "bytes_written", "replacement_count", "mode",
		"patch_bytes", "changed_path_count", "created_paths", "deleted_paths", "hunk_count",
		"truncated", "binary":
		return true
	default:
		return false
	}
}

func parseToolMetadataValue(value string) any {
	if strings.EqualFold(value, "true") {
		return true
	}
	if strings.EqualFold(value, "false") {
		return false
	}
	if n, err := strconv.Atoi(value); err == nil {
		return n
	}
	return value
}

func classifyToolResultError(output string) executorErrorCode {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "timed_out: true") || strings.Contains(lower, "local_timeout"):
		return errToolTimeout
	case strings.Contains(lower, "interrupted: true") || strings.Contains(lower, "local_interrupted"):
		return errStopped
	case strings.Contains(lower, "local_spawn_failed") ||
		strings.Contains(lower, "stdout_pipe:") ||
		strings.Contains(lower, "stderr_pipe:"):
		return errToolInfra
	default:
		return ""
	}
}

func (t *nativeTools) readFile(p string, startLine, limitLines int) (string, error) {
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	t.logOutsideWorkspaceAccess("read_file", full, false)
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", p)
	}
	if startLine <= 0 {
		startLine = 1
	}
	if limitLines <= 0 {
		limitLines = defaultToolReadLines
	}
	if limitLines > t.limit.MaxLines {
		limitLines = t.limit.MaxLines
	}
	content, totalLines, endLine, truncatedBytes, binary, err := readTextFileRange(full, startLine, limitLines, t.limit.MaxBytes)
	if err != nil {
		return "", err
	}
	if binary {
		return fmt.Sprintf("path: %s\nsize_bytes: %d\nbinary: true\ncontent:\n(binary file omitted)", t.displayPath(full), info.Size()), nil
	}
	if endLine < startLine {
		endLine = startLine - 1
	}
	truncated := endLine < totalLines || truncatedBytes
	return fmt.Sprintf("path: %s\nsize_bytes: %d\nlines_returned: %d-%d\nline_count: %d\ntruncated: %t\ncontent:\n%s",
		t.displayPath(full), info.Size(), startLine, endLine, totalLines, truncated, content), nil
}

func readTextFileRange(full string, startLine, limitLines, maxBytes int) (string, int, int, bool, bool, error) {
	f, err := os.Open(full)
	if err != nil {
		return "", 0, 0, false, false, err
	}
	defer f.Close()

	endLine := startLine + limitLines - 1
	totalLines := 0
	lastReturned := startLine - 1
	truncatedBytes := false
	var out strings.Builder
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if !isUTF8TextBytes(fragment) {
				return "", totalLines, lastReturned, false, true, nil
			}
			currentLine := totalLines + 1
			if currentLine >= startLine && currentLine <= endLine {
				lastReturned = currentLine
				if !truncatedBytes {
					remaining := maxBytes - out.Len()
					if remaining <= 0 {
						truncatedBytes = true
					} else if len(fragment) > remaining {
						out.Write(fragment[:validUTF8PrefixLen(fragment, remaining)])
						truncatedBytes = true
					} else {
						out.Write(fragment)
					}
				}
			}
			if readErr != bufio.ErrBufferFull {
				totalLines++
			}
		}
		switch readErr {
		case nil, bufio.ErrBufferFull:
			continue
		case io.EOF:
			return out.String(), totalLines, lastReturned, truncatedBytes, false, nil
		default:
			return "", totalLines, lastReturned, truncatedBytes, false, readErr
		}
	}
}

func (t *nativeTools) write(p, content string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.ContainsRune(content, '\x00') {
		return "", fmt.Errorf("content contains NUL byte")
	}
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	t.logOutsideWorkspaceAccess("write_file", full, true)
	created := false
	if _, statErr := os.Stat(full); os.IsNotExist(statErr) {
		created = true
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	mode := ""
	if info, err := os.Stat(full); err == nil {
		mode = fmt.Sprintf("\nmode: %s", info.Mode().String())
	}
	return fmt.Sprintf("path: %s\ncreated: %t\nbytes_written: %d%s", t.displayPath(full), created, len(content), mode), nil
}

func (t *nativeTools) edit(p, oldString, newString string, replaceAll bool, expectedCount int) (string, error) {
	if oldString == "" {
		return "", fmt.Errorf("old_string is required")
	}
	if strings.ContainsRune(oldString, '\x00') || strings.ContainsRune(newString, '\x00') {
		return "", fmt.Errorf("old_string and new_string must not contain NUL bytes")
	}
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	t.logOutsideWorkspaceAccess("replace_text", full, true)
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if !isUTF8TextBytes(data) {
		return "", fmt.Errorf("%s is not a UTF-8 text file", p)
	}
	text := string(data)
	count := strings.Count(text, oldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", p)
	}
	if expectedCount > 0 && count != expectedCount {
		return "", fmt.Errorf("replacement count mismatch in %s: found %d, expected %d", p, count, expectedCount)
	}
	n := 1
	if replaceAll {
		n = -1
	} else if expectedCount > 1 {
		return "", fmt.Errorf("expected_count=%d requires replace_all=true", expectedCount)
	}
	updated := strings.Replace(text, oldString, newString, n)
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", err
	}
	if !replaceAll {
		count = 1
	}
	mode := ""
	if info, err := os.Stat(full); err == nil {
		mode = fmt.Sprintf("\nmode: %s", info.Mode().String())
	}
	return fmt.Sprintf("path: %s\nreplacement_count: %d\nbytes_written: %d\ncreated: false%s", t.displayPath(full), count, len(updated), mode), nil
}

func (t *nativeTools) applyPatch(ctx context.Context, patchText string) (string, error) {
	if strings.TrimSpace(patchText) == "" {
		return "", fmt.Errorf("patch is required")
	}
	tmp, err := os.CreateTemp("", "telos-patch-*.diff")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(patchText); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	changed := patchChangedPaths(patchText)
	if err := validatePatchPaths(patchDeclaredPaths(patchText)); err != nil {
		return "", err
	}
	created := patchCreatedPaths(patchText)
	deleted := patchDeletedPaths(patchText)
	hunks := patchHunkCount(patchText)
	cmd := "git apply --whitespace=nowarn --recount " + shellQuote(tmp.Name())
	out, err := t.bash(ctx, cmd, "", nil, defaultToolTimeoutSec)
	if err != nil {
		return out, err
	}
	lines := []string{
		fmt.Sprintf("patch_bytes: %d", len(patchText)),
		fmt.Sprintf("changed_path_count: %d", len(changed)),
		fmt.Sprintf("changed_paths: %s", strings.Join(changed, ", ")),
		fmt.Sprintf("created_paths: %s", strings.Join(created, ", ")),
		fmt.Sprintf("deleted_paths: %s", strings.Join(deleted, ", ")),
		fmt.Sprintf("hunk_count: %d", hunks),
		"files:",
		t.patchFileMetadata(changed, created, deleted),
		out,
	}
	return strings.Join(lines, "\n"), nil
}

func (t *nativeTools) patchFileMetadata(changed, created, deleted []string) string {
	createdSet := stringSet(created)
	deletedSet := stringSet(deleted)
	var lines []string
	for _, p := range changed {
		full, err := t.resolvePath(p)
		if err != nil {
			lines = append(lines, fmt.Sprintf("- path: %s\n  error: %s", p, err))
			continue
		}
		if deletedSet[p] {
			lines = append(lines, fmt.Sprintf("- path: %s\n  created: false\n  deleted: true\n  bytes_written: 0", t.displayPath(full)))
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			lines = append(lines, fmt.Sprintf("- path: %s\n  created: %t\n  deleted: false\n  error: %s", t.displayPath(full), createdSet[p], err))
			continue
		}
		lines = append(lines, fmt.Sprintf("- path: %s\n  created: %t\n  deleted: false\n  bytes_written: %d\n  mode: %s", t.displayPath(full), createdSet[p], info.Size(), info.Mode().String()))
	}
	if len(lines) == 0 {
		return "none"
	}
	return strings.Join(lines, "\n")
}

func (t *nativeTools) bash(ctx context.Context, command string, cwd string, env map[string]string, timeout int) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command is required")
	}
	if timeout <= 0 || timeout > defaultToolTimeoutSec {
		timeout = defaultToolTimeoutSec
	}
	// Honor both the explicit stop request and the turn deadline carried by ctx.
	interrupt := func() bool {
		if ctx.Err() != nil {
			return true
		}
		return t.stopRequested != nil && t.stopRequested()
	}
	runCWD := ""
	if strings.TrimSpace(cwd) != "" {
		full, err := t.resolvePath(cwd)
		if err != nil {
			return "", err
		}
		runCWD = full
	}
	result := t.platform.Run([]string{"bash", "-lc", command}, "", env, timeout, interrupt, nil, runCWD)
	stdoutText, stdoutLineTruncated := capOutputLines(strings.Join(result.RawLines, "\n"), "stdout", result.StdoutOriginalLines, t.limit.MaxLines)
	stderrText, stderrLineTruncated := capOutputLines(strings.TrimSpace(result.Stderr), "stderr", result.StderrOriginalLines, t.limit.MaxLines)
	stdoutTruncated := result.StdoutTruncated || stdoutLineTruncated
	stderrTruncated := result.StderrTruncated || stderrLineTruncated
	var parts []string
	parts = append(parts,
		fmt.Sprintf("exit_code: %d", result.ReturnCode),
		fmt.Sprintf("signal: %s", defaultString(result.Signal, "none")),
		fmt.Sprintf("started_at: %s", result.StartedAt.UTC().Format(time.RFC3339Nano)),
		fmt.Sprintf("ended_at: %s", result.EndedAt.UTC().Format(time.RFC3339Nano)),
		fmt.Sprintf("duration_ms: %d", result.DurationMS),
		fmt.Sprintf("timed_out: %t", result.TimedOut),
		fmt.Sprintf("interrupted: %t", result.Interrupted),
		fmt.Sprintf("stdout_bytes: %d", result.StdoutBytes),
		fmt.Sprintf("stdout_original_bytes: %d", result.StdoutOriginalBytes),
		fmt.Sprintf("stdout_original_lines: %d", result.StdoutOriginalLines),
		fmt.Sprintf("stdout_truncated: %t", stdoutTruncated),
		fmt.Sprintf("stderr_bytes: %d", result.StderrBytes),
		fmt.Sprintf("stderr_original_bytes: %d", result.StderrOriginalBytes),
		fmt.Sprintf("stderr_original_lines: %d", result.StderrOriginalLines),
		fmt.Sprintf("stderr_truncated: %t", stderrTruncated),
	)
	if stdoutText != "" {
		parts = append(parts, "stdout:\n"+stdoutText)
	}
	if stderrText != "" {
		parts = append(parts, "stderr:\n"+stderrText)
	}
	if result.InfraError != "" {
		parts = append(parts, "error:\n"+result.InfraError)
		return strings.Join(parts, "\n"), errors.New(strings.Join(parts, "\n"))
	}
	if result.ReturnCode != 0 {
		return strings.Join(parts, "\n"), errors.New(strings.Join(parts, "\n"))
	}
	return strings.Join(parts, "\n"), nil
}

func capOutputLines(text string, streamName string, originalLines int, maxLines int) (string, bool) {
	if strings.TrimSpace(text) == "" || maxLines <= 0 {
		return text, false
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text, false
	}
	displayOriginal := originalLines
	if displayOriginal < len(lines) {
		displayOriginal = len(lines)
	}
	lines = append(lines[:maxLines], fmt.Sprintf("... %s truncated at %d lines of %d ...", streamName, maxLines, displayOriginal))
	return strings.Join(lines, "\n"), true
}

func (t *nativeTools) ls(p string) (string, error) {
	full, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	t.logOutsideWorkspaceAccess("list_dir", full, false)
	dir, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	var lines []string
	entryCount := 0
	for {
		entries, err := dir.ReadDir(256)
		if len(entries) > 0 {
			for _, entry := range entries {
				entryCount++
				if len(lines) >= t.limit.MaxLines {
					continue
				}
				name := entry.Name()
				if entry.IsDir() {
					name += "/"
				}
				lines = append(lines, name)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	sort.Strings(lines)
	entriesText, truncatedBytes := truncateText(strings.Join(lines, "\n"), t.limit.MaxBytes)
	truncated := entryCount > len(lines) || truncatedBytes
	return fmt.Sprintf("path: %s\nentry_count: %d\nentries_returned: %d\ntruncated: %t\nentries:\n%s", t.displayPath(full), entryCount, len(lines), truncated, entriesText), nil
}

func (t *nativeTools) grep(pattern, p string, maxMatches int) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if maxMatches <= 0 || maxMatches > 500 {
		maxMatches = 100
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	t.logOutsideWorkspaceAccess("search_text", root, false)
	var matches []string
	visit := func(file string) {
		if len(matches) >= maxMatches {
			return
		}
		info, err := os.Stat(file)
		if err != nil || info.IsDir() || info.Size() > 2<<20 {
			return
		}
		data, err := os.ReadFile(file)
		if err != nil || !isUTF8TextBytes(data) {
			return
		}
		rel := t.displayPath(file)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				line, _ = truncateText(line, defaultToolSearchLineBytes)
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, line))
				if len(matches) >= maxMatches {
					break
				}
			}
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		visit(root)
	} else {
		_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if len(matches) >= maxMatches {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if shouldSkipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			visit(file)
			return nil
		})
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	out := strings.Join(matches, "\n")
	out, truncatedBytes := truncateText(out, t.limit.MaxBytes)
	truncated := len(matches) >= maxMatches || truncatedBytes
	return fmt.Sprintf("match_count: %d\ntruncated: %t\nmatches:\n%s", len(matches), truncated, out), nil
}

func (t *nativeTools) find(pattern, p string, maxMatches int) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if err := doublestar.ValidatePattern(pattern); !err {
		return "", fmt.Errorf("invalid glob pattern %q", pattern)
	}
	if maxMatches <= 0 || maxMatches > 1000 {
		maxMatches = 200
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	t.logOutsideWorkspaceAccess("find_files", root, false)
	var matches []string
	_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(matches) >= maxMatches {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel := t.displayPath(file)
		// Match the full relative path first so recursive patterns like
		// "**/*.go" or "src/**/foo_*.txt" work. Fall back to the basename so a
		// bare pattern like "*.go" still matches files at any depth — this is
		// the convenience behavior users expect from `find -name`.
		base := path.Base(rel)
		if ok, _ := doublestar.Match(pattern, rel); ok {
			matches = append(matches, rel)
		} else if ok, _ := doublestar.Match(pattern, base); ok {
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	if len(matches) == 0 {
		return "no matches", nil
	}
	pathsText, truncatedBytes := truncateText(strings.Join(matches, "\n"), t.limit.MaxBytes)
	truncated := len(matches) >= maxMatches || truncatedBytes
	return fmt.Sprintf("match_count: %d\ntruncated: %t\npaths:\n%s", len(matches), truncated, pathsText), nil
}

func (t *nativeTools) fileInfo(p string) (string, error) {
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	t.logOutsideWorkspaceAccess("file_info", full, false)
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	lineCount := ""
	if info.Mode().IsRegular() && info.Size() <= 8<<20 {
		if data, err := os.ReadFile(full); err == nil && isUTF8TextBytes(data) {
			lineCount = fmt.Sprintf("\nline_count: %d", len(strings.Split(string(data), "\n")))
		}
	}
	return fmt.Sprintf("path: %s\ntype: %s\nsize_bytes: %d\nmode: %s%s", t.displayPath(full), kind, info.Size(), info.Mode().String(), lineCount), nil
}

func (t *nativeTools) skill(action, name, refPath string, startLine, limitLines int) (string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list":
		if len(t.skills) == 0 {
			return "skills: none", nil
		}
		names := make([]string, 0, len(t.skills))
		for name := range t.skills {
			names = append(names, name)
		}
		sort.Strings(names)
		var lines []string
		for _, skillName := range names {
			ref := t.skills[skillName]
			required := ""
			if ref.Required {
				required = "\nrequired: true"
			}
			lines = append(lines, fmt.Sprintf("name: %s%s\ndescription: %s\npath: %s", ref.Name, required, ref.Description, ref.Path))
		}
		return strings.Join(lines, "\n---\n"), nil
	case "read", "read_ref":
		ref, ok := t.skills[strings.TrimSpace(name)]
		if !ok {
			return "", fmt.Errorf("unknown skill %q; use action=list to inspect available skills", name)
		}
		readPath := ref.Path
		if action == "read_ref" {
			var err error
			readPath, err = resolveSkillReferencePath(ref.Path, refPath)
			if err != nil {
				return "", err
			}
		}
		read, err := t.readSkillFile(readPath, startLine, limitLines)
		if err != nil {
			return "", err
		}
		// A required rubric is satisfied once the model has read it in full, even
		// across several paginated reads. Track contiguous coverage from line 1
		// so a rubric larger than one read window can still be completed by
		// walking start_line to EOF, rather than being permanently "unread". A
		// byte-truncated read does not advance coverage, since its window was cut
		// mid-content and those lines were not all delivered.
		if action == "read" && !read.binary && !read.byteTruncated {
			covered := t.skillCoverage[ref.Name]
			if read.startLine <= covered+1 && read.endLine > covered {
				covered = read.endLine
				t.skillCoverage[ref.Name] = covered
			}
			fullyRead := read.totalLines > 0 && covered >= read.totalLines
			if fullyRead && !t.openedSkills[ref.Name] {
				t.openedSkills[ref.Name] = true
				_ = t.logger.skillApplied(ref.Name, readPath)
			}
		}
		_ = t.logger.skillOpened(ref.Name, readPath, read.truncated)
		return fmt.Sprintf("name: %s\npath: %s\ntruncated: %t\n%s", ref.Name, readPath, read.truncated, read.body), nil
	default:
		return "", fmt.Errorf("unknown skill action %q; use 'list', 'read', or 'read_ref'", action)
	}
}

// skillReadResult carries the rendered skill body plus the line range read, so
// callers can track how much of a required rubric has been covered.
type skillReadResult struct {
	body       string
	startLine  int
	endLine    int
	totalLines int
	truncated  bool
	// byteTruncated is true when the returned content was cut mid-window by the
	// byte cap, so lines [startLine, endLine] were not all actually delivered.
	byteTruncated bool
	binary        bool
}

func (t *nativeTools) readSkillFile(full string, startLine, limitLines int) (skillReadResult, error) {
	info, err := os.Stat(full)
	if err != nil {
		return skillReadResult{}, err
	}
	if info.IsDir() {
		return skillReadResult{}, fmt.Errorf("%s is a directory", full)
	}
	if startLine <= 0 {
		startLine = 1
	}
	if limitLines <= 0 {
		limitLines = defaultToolReadLines
	}
	if limitLines > t.limit.MaxLines {
		limitLines = t.limit.MaxLines
	}
	content, totalLines, endLine, truncatedBytes, binary, err := readTextFileRange(full, startLine, limitLines, t.limit.MaxBytes)
	if err != nil {
		return skillReadResult{}, err
	}
	if binary {
		return skillReadResult{
			body:   fmt.Sprintf("size_bytes: %d\nbinary: true\ncontent:\n(binary file omitted)", info.Size()),
			binary: true,
		}, nil
	}
	if endLine < startLine {
		endLine = startLine - 1
	}
	truncated := endLine < totalLines || truncatedBytes
	body := fmt.Sprintf("size_bytes: %d\nlines_returned: %d-%d\nline_count: %d\ncontent:\n%s", info.Size(), startLine, endLine, totalLines, content)
	return skillReadResult{
		body:          body,
		startLine:     startLine,
		endLine:       endLine,
		totalLines:    totalLines,
		truncated:     truncated,
		byteTruncated: truncatedBytes,
	}, nil
}

func resolveSkillReferencePath(skillPath, refPath string) (string, error) {
	refPath = strings.TrimSpace(refPath)
	if refPath == "" {
		return "", fmt.Errorf("path is required for skill action='read_ref'")
	}
	refPath = filepath.FromSlash(refPath)
	if filepath.IsAbs(refPath) {
		return "", fmt.Errorf("skill reference path %q must be relative", refPath)
	}
	base, err := filepath.Abs(filepath.Dir(skillPath))
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(base, filepath.Clean(refPath)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("skill reference path %q is outside skill directory", refPath)
	}
	return full, nil
}

func (t *nativeTools) missingRequiredSkills() []string {
	if t == nil {
		return nil
	}
	var missing []string
	for name, ref := range t.skills {
		if ref.Required && !t.openedSkills[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func (t *nativeTools) resolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	workspace, err := filepath.Abs(t.platform.Workspace)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(p) {
		return filepath.Abs(filepath.Clean(p))
	}
	full, err := filepath.Abs(filepath.Join(workspace, p))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspace, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside workspace %q", p, workspace)
	}
	return full, nil
}

func (t *nativeTools) isInsideWorkspace(full string) bool {
	workspace, err := filepath.Abs(t.platform.Workspace)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(workspace, full)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (t *nativeTools) displayPath(full string) string {
	workspace, err := filepath.Abs(t.platform.Workspace)
	if err == nil {
		if rel, relErr := filepath.Rel(workspace, full); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(full)
}

func (t *nativeTools) logOutsideWorkspaceAccess(action, full string, write bool) {
	if t == nil || t.isInsideWorkspace(full) {
		return
	}
	_ = t.logger.outsideWorkspaceAccess(action, t.displayPath(full), write)
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".telos", "__pycache__", "node_modules", ".venv", "venv":
		return true
	default:
		return false
	}
}

// validateToolArgs rejects a tool call whose supplied arguments have the wrong
// JSON type for the parameter, so a model that sends e.g. start_line: "10"
// (string) gets a clear protocol error instead of the value being silently
// coerced to the zero value by argInt. Only present, non-null, declared
// parameters are checked; missing required fields are still reported by the
// individual handlers with parameter-specific messages.
func validateToolArgs(tool nativeTool, args map[string]interface{}) error {
	props, ok := tool.parameters["properties"].(map[string]interface{})
	if !ok {
		return nil
	}
	for key, raw := range args {
		if raw == nil {
			continue
		}
		spec, ok := props[key].(map[string]interface{})
		if !ok {
			continue
		}
		want, _ := spec["type"].(string)
		if !argMatchesType(raw, want) {
			return fmt.Errorf("argument %q must be of type %s", key, want)
		}
	}
	return nil
}

func argMatchesType(value any, want string) bool {
	switch want {
	case "string":
		_, ok := value.(string)
		return ok
	case "integer", "number":
		_, ok := value.(float64)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]interface{})
		return ok
	default:
		return true
	}
}

func argString(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return value
}

func argBool(args map[string]interface{}, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func argInt(args map[string]interface{}, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

var shellEnvNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func argEnv(args map[string]interface{}, key string) (map[string]string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	if len(values) > 32 {
		return nil, fmt.Errorf("%s has too many variables: %d > 32", key, len(values))
	}
	env := make(map[string]string, len(values))
	for name, value := range values {
		if !shellEnvNameRE.MatchString(name) {
			return nil, fmt.Errorf("invalid environment variable name %q", name)
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("environment variable %q must be a string", name)
		}
		if len(text) > 4096 {
			return nil, fmt.Errorf("environment variable %q is too large", name)
		}
		if strings.ContainsRune(text, 0) {
			return nil, fmt.Errorf("environment variable %q contains a NUL byte", name)
		}
		env[name] = text
	}
	return env, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func formatToolEnvelope(name string, ok bool, durationMS int64, body string) string {
	return fmt.Sprintf("tool: %s\nok: %t\nduration_ms: %d\n%s", name, ok, durationMS, body)
}

func truncateText(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	end := validUTF8PrefixLen([]byte(text), maxBytes)
	return text[:end] + fmt.Sprintf("\n... truncated %d bytes ...", len(text)-end), true
}

func isUTF8TextBytes(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0 && utf8.Valid(data)
}

func validUTF8PrefixLen(data []byte, maxBytes int) int {
	if maxBytes <= 0 {
		return 0
	}
	if maxBytes >= len(data) {
		return len(data)
	}
	end := maxBytes
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	return end
}

func envInt(name string, fallback int, min int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min {
		return fallback
	}
	return n
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func patchChangedPaths(patchText string) []string {
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(patchText, "\n") {
		var p string
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			p = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "--- a/"):
			p = strings.TrimPrefix(line, "--- a/")
		}
		if p == "" || p == "/dev/null" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func patchDeclaredPaths(patchText string) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		p = normalizePatchPath(p)
		if p == "" || p == "/dev/null" || seen[p] {
			return
		}
		seen[p] = true
		paths = append(paths, p)
	}
	for _, line := range strings.Split(patchText, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- "):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				add(fields[1])
			}
		case strings.HasPrefix(line, "diff --git "):
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				add(fields[2])
				add(fields[3])
			}
		case strings.HasPrefix(line, "rename from "):
			add(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			add(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "copy from "):
			add(strings.TrimPrefix(line, "copy from "))
		case strings.HasPrefix(line, "copy to "):
			add(strings.TrimPrefix(line, "copy to "))
		}
	}
	sort.Strings(paths)
	return paths
}

func normalizePatchPath(p string) string {
	p = strings.TrimSpace(p)
	if unquoted, err := strconv.Unquote(p); err == nil {
		p = unquoted
	}
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return p
}

func validatePatchPaths(paths []string) error {
	for _, p := range paths {
		clean := path.Clean(strings.TrimSpace(p))
		if clean == "." || clean == "" {
			return fmt.Errorf("patch contains empty path")
		}
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("patch path %q is outside workspace", p)
		}
	}
	return nil
}

func patchCreatedPaths(patchText string) []string {
	var paths []string
	seen := map[string]bool{}
	lines := strings.Split(patchText, "\n")
	for i := 0; i+1 < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "--- /dev/null" || !strings.HasPrefix(lines[i+1], "+++ b/") {
			continue
		}
		p := strings.TrimPrefix(lines[i+1], "+++ b/")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func patchDeletedPaths(patchText string) []string {
	var paths []string
	seen := map[string]bool{}
	lines := strings.Split(patchText, "\n")
	for i := 0; i+1 < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "--- a/") || strings.TrimSpace(lines[i+1]) != "+++ /dev/null" {
			continue
		}
		p := strings.TrimPrefix(lines[i], "--- a/")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func patchHunkCount(patchText string) int {
	count := 0
	for _, line := range strings.Split(patchText, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			count++
		}
	}
	return count
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

func oxfordList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}
