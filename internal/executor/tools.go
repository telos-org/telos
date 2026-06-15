package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/telos-org/telos/internal/platform"
)

const defaultToolTimeoutSec = 120

type nativeToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type nativeToolResult struct {
	CallID  string
	Name    string
	Output  string
	IsError bool
}

// nativeTool is the single source of truth for a tool: its schema and its
// handler. Schema generation, dispatch, and the system-prompt name list all
// derive from the same table.
type nativeTool struct {
	name        string
	description string
	parameters  map[string]interface{}
	run         func(t *nativeTools, ctx context.Context, args map[string]interface{}) (string, error)
}

func nativeToolTable() []nativeTool {
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
			name:        "read",
			description: "Read a UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"path"}, map[string]interface{}{"path": str("Relative workspace path or absolute container path.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.read(argString(args, "path"))
			},
		},
		{
			name:        "write",
			description: "Create or overwrite a UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"path", "content"}, map[string]interface{}{"path": str("Relative workspace path or absolute container path."), "content": str("Complete file content to write.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.write(argString(args, "path"), argString(args, "content"))
			},
		},
		{
			name:        "edit",
			description: "Replace text in an existing UTF-8 file. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"path", "old_string", "new_string"}, map[string]interface{}{"path": str("Relative workspace path or absolute container path."), "old_string": str("Exact text to replace."), "new_string": str("Replacement text."), "replace_all": boolean("Replace every occurrence instead of only the first.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.edit(argString(args, "path"), argString(args, "old_string"), argString(args, "new_string"), argBool(args, "replace_all"))
			},
		},
		{
			name:        "bash",
			description: "Run a shell command in the workspace.",
			parameters:  obj([]string{"command"}, map[string]interface{}{"command": str("Command to run with bash -lc."), "timeout_seconds": integer("Optional timeout, capped by Telos.")}),
			run: func(t *nativeTools, ctx context.Context, args map[string]interface{}) (string, error) {
				return t.bash(ctx, argString(args, "command"), argInt(args, "timeout_seconds"))
			},
		},
		{
			name:        "ls",
			description: "List files in a directory. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{}, map[string]interface{}{"path": str("Directory path, defaults to workspace root.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.ls(argString(args, "path"))
			},
		},
		{
			name:        "grep",
			description: "Search text files with a regular expression. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"pattern"}, map[string]interface{}{"pattern": str("Go regular expression."), "path": str("Directory or file path, defaults to workspace root."), "max_matches": integer("Maximum matches to return.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.grep(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
			},
		},
		{
			name:        "find",
			description: "Find files by glob pattern. Relative paths resolve inside the workspace; absolute paths are used as-is.",
			parameters:  obj([]string{"pattern"}, map[string]interface{}{"pattern": str("Glob pattern matched against relative paths and basenames."), "path": str("Directory path, defaults to workspace root."), "max_matches": integer("Maximum paths to return.")}),
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (string, error) {
				return t.find(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
			},
		},
	}
}

func nativeToolNames() []string {
	table := nativeToolTable()
	names := make([]string, len(table))
	for i, def := range table {
		names[i] = def.name
	}
	return names
}

func toolSchemasForChat() []map[string]interface{} {
	defs := nativeToolTable()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        def.name,
				"description": def.description,
				"parameters":  def.parameters,
			},
		})
	}
	return out
}

func toolSchemasForResponses() []map[string]interface{} {
	defs := nativeToolTable()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"type":        "function",
			"name":        def.name,
			"description": def.description,
			"parameters":  def.parameters,
		})
	}
	return out
}

func toolSchemasForAnthropic() []map[string]interface{} {
	defs := nativeToolTable()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		out = append(out, map[string]interface{}{
			"name":         def.name,
			"description":  def.description,
			"input_schema": def.parameters,
		})
	}
	return out
}

type nativeTools struct {
	platform      *platform.LocalPlatform
	stopRequested func() bool
	byName        map[string]nativeTool
}

func newNativeTools(p *platform.LocalPlatform, stopRequested func() bool) *nativeTools {
	t := &nativeTools{platform: p, stopRequested: stopRequested, byName: map[string]nativeTool{}}
	for _, tool := range nativeToolTable() {
		t.byName[tool.name] = tool
	}
	return t
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
			return nativeToolResult{CallID: call.ID, Name: call.Name, Output: "invalid tool arguments: " + err.Error(), IsError: true}
		}
	}
	tool, ok := t.byName[call.Name]
	if !ok {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: fmt.Sprintf("unknown tool %q; available tools are %s", call.Name, oxfordList(nativeToolNames())), IsError: true}
	}
	output, err := tool.run(t, ctx, args)
	if err != nil {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: err.Error(), IsError: true}
	}
	return nativeToolResult{CallID: call.ID, Name: call.Name, Output: output}
}

func (t *nativeTools) read(p string) (string, error) {
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *nativeTools) write(p, content string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	return "wrote " + t.displayPath(full), nil
}

func (t *nativeTools) edit(p, oldString, newString string, replaceAll bool) (string, error) {
	if oldString == "" {
		return "", fmt.Errorf("old_string is required")
	}
	full, err := t.resolvePath(p)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	text := string(data)
	count := strings.Count(text, oldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s", p)
	}
	n := 1
	if replaceAll {
		n = -1
	}
	updated := strings.Replace(text, oldString, newString, n)
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", err
	}
	if !replaceAll {
		count = 1
	}
	return fmt.Sprintf("edited %s (%d replacement%s)", p, count, plural(count)), nil
}

func (t *nativeTools) bash(ctx context.Context, command string, timeout int) (string, error) {
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
	result := t.platform.Run([]string{"bash", "-lc", command}, "", nil, timeout, interrupt, nil)
	var parts []string
	if stdout := strings.Join(result.RawLines, "\n"); stdout != "" {
		parts = append(parts, "[stdout]\n"+stdout)
	}
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		parts = append(parts, "[stderr]\n"+stderr)
	}
	if result.InfraError != "" {
		parts = append(parts, "[error]\n"+result.InfraError)
		return strings.Join(parts, "\n"), errors.New(strings.Join(parts, "\n"))
	}
	if result.ReturnCode != 0 {
		parts = append(parts, fmt.Sprintf("[exit_code]\n%d", result.ReturnCode))
	}
	if len(parts) == 0 {
		return "command completed with no output", nil
	}
	return strings.Join(parts, "\n"), nil
}

func (t *nativeTools) ls(p string) (string, error) {
	full, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
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
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return
		}
		rel := t.displayPath(file)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
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
	return strings.Join(matches, "\n"), nil
}

func (t *nativeTools) find(pattern, p string, maxMatches int) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if maxMatches <= 0 || maxMatches > 1000 {
		maxMatches = 200
	}
	root, err := t.resolvePath(defaultString(p, "."))
	if err != nil {
		return "", err
	}
	var matches []string
	_ = filepath.WalkDir(root, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel := t.displayPath(file)
		base := path.Base(rel)
		if ok, _ := path.Match(pattern, rel); ok {
			matches = append(matches, rel)
		} else if ok, _ := path.Match(pattern, base); ok {
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	if len(matches) > maxMatches {
		matches = matches[:maxMatches]
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	return strings.Join(matches, "\n"), nil
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

func (t *nativeTools) displayPath(full string) string {
	workspace, err := filepath.Abs(t.platform.Workspace)
	if err == nil {
		if rel, relErr := filepath.Rel(workspace, full); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(full)
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".telos", "__pycache__", "node_modules", ".venv", "venv":
		return true
	default:
		return false
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

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
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
