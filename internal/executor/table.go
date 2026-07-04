package executor

import (
	"context"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
	"github.com/telos-org/telos/internal/executor/providercore"
)

type nativeTool struct {
	name        string
	aliases     []string
	description string
	parameters  map[string]interface{}
	metadata    nativeToolMetadata
	run         func(t *nativeTools, ctx context.Context, args map[string]interface{}) (toolOutput, error)
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
	enumString := func(desc string, values ...string) map[string]interface{} {
		enum := make([]interface{}, 0, len(values))
		for _, value := range values {
			enum = append(enum, value)
		}
		return map[string]interface{}{"type": "string", "description": desc, "enum": enum}
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
			description: "Read a bounded UTF-8 file range. Paths are workspace-relative; absolute paths are accepted only when they resolve inside the workspace.",
			parameters:  obj([]string{"path"}, map[string]interface{}{"path": str("Workspace-relative path."), "start_line": integer("Optional 1-based start line."), "limit_lines": integer("Optional maximum lines to return.")}),
			metadata:    nativeToolMetadata{sideEffect: toolRead, pathArgs: []nativeToolPathArg{{name: "path", mode: toolPathRead}}},
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (toolOutput, error) {
				return t.readFile(argString(args, "path"), argInt(args, "start_line"), argInt(args, "limit_lines"))
			},
		},
		{
			name:        "write_file",
			aliases:     []string{"write"},
			description: "Create or overwrite a UTF-8 file inside the workspace.",
			parameters:  obj([]string{"path", "content"}, map[string]interface{}{"path": str("Workspace-relative path."), "content": str("Complete file content to write.")}),
			metadata:    nativeToolMetadata{sideEffect: toolMutate, pathArgs: []nativeToolPathArg{{name: "path", mode: toolPathWrite}}, redactArgs: []string{"content"}, changedFiles: true, previewOutputs: true},
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (toolOutput, error) {
				return t.write(argString(args, "path"), argString(args, "content"))
			},
		},
		{
			name:        "replace_text",
			aliases:     []string{"edit"},
			description: "Replace text in an existing UTF-8 file with an optional exact expected replacement count.",
			parameters:  obj([]string{"path", "old_string", "new_string"}, map[string]interface{}{"path": str("Workspace-relative path."), "old_string": str("Exact text to replace."), "new_string": str("Replacement text."), "replace_all": boolean("Replace every occurrence instead of only the first."), "expected_count": integer("Optional required replacement count.")}),
			metadata:    nativeToolMetadata{sideEffect: toolMutate, pathArgs: []nativeToolPathArg{{name: "path", mode: toolPathWrite}}, redactArgs: []string{"old_string", "new_string"}, changedFiles: true, previewOutputs: true},
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (toolOutput, error) {
				return t.edit(argString(args, "path"), argString(args, "old_string"), argString(args, "new_string"), argBool(args, "replace_all"), argInt(args, "expected_count"))
			},
		},
		{
			name:        "apply_patch",
			description: "Apply a unified diff patch to the workspace. Prefer this for line-oriented multi-file edits.",
			parameters:  obj([]string{"patch"}, map[string]interface{}{"patch": str("Unified diff patch text.")}),
			metadata:    nativeToolMetadata{sideEffect: toolMutate, redactArgs: []string{"patch"}, changedFiles: true, previewOutputs: true},
			run: func(t *nativeTools, ctx context.Context, args map[string]interface{}) (toolOutput, error) {
				return t.applyPatch(ctx, argString(args, "patch"))
			},
		},
		{
			name:        "bash",
			description: "Run a shell command in the workspace with bounded output and optional workspace-relative cwd/env. timeout_seconds defaults to 120 seconds and is capped by the effective turn duration budget.",
			parameters: obj([]string{"command"}, map[string]interface{}{
				"command":         str("Command to run with bash -lc."),
				"timeout_seconds": integer("Optional timeout, capped by Telos."),
				"cwd":             str("Optional workspace-relative working directory."),
				"env": map[string]interface{}{
					"type":                 "object",
					"description":          "Optional per-command environment variables. Names must match [A-Za-z_][A-Za-z0-9_]*.",
					"additionalProperties": map[string]interface{}{"type": "string"},
				},
			}),
			metadata: nativeToolMetadata{sideEffect: toolExecute, pathArgs: []nativeToolPathArg{{name: "cwd", mode: toolPathRead, optional: true}}, redactArgs: []string{"command", "env"}, redactOutputs: []string{"stdout", "stderr"}},
			run: func(t *nativeTools, ctx context.Context, args map[string]interface{}) (toolOutput, error) {
				env, err := argEnv(args, "env")
				if err != nil {
					return toolOutput{}, err
				}
				return t.bash(ctx, argString(args, "command"), argString(args, "cwd"), env, argInt(args, "timeout_seconds"))
			},
		},
		{
			name:        "list_dir",
			aliases:     []string{"ls"},
			description: "List a bounded workspace directory.",
			parameters:  obj([]string{}, map[string]interface{}{"path": str("Workspace-relative directory path, defaults to workspace root.")}),
			metadata:    nativeToolMetadata{sideEffect: toolRead, pathArgs: []nativeToolPathArg{{name: "path", mode: toolPathRead, optional: true}}},
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (toolOutput, error) {
				return t.ls(argString(args, "path"))
			},
		},
		{
			name:        "search_text",
			aliases:     []string{"grep"},
			description: "Search text files with a regular expression and bounded match output.",
			parameters:  obj([]string{"pattern"}, map[string]interface{}{"pattern": str("Go regular expression."), "path": str("Workspace-relative directory or file path, defaults to workspace root."), "max_matches": integer("Maximum matches to return.")}),
			metadata:    nativeToolMetadata{sideEffect: toolRead, pathArgs: []nativeToolPathArg{{name: "path", mode: toolPathRead, optional: true}}},
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (toolOutput, error) {
				return t.grep(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
			},
		},
		{
			name:        "find_files",
			aliases:     []string{"find"},
			description: "Find files by glob pattern with bounded output. Supports `**` for recursive directory matches (e.g. `**/*.go`, `src/**/foo_*.txt`); a bare pattern like `*.go` matches files at any depth.",
			parameters:  obj([]string{"pattern"}, map[string]interface{}{"pattern": str("Glob pattern matched against relative paths and basenames; supports `**`."), "path": str("Workspace-relative directory path, defaults to workspace root."), "max_matches": integer("Maximum paths to return.")}),
			metadata:    nativeToolMetadata{sideEffect: toolRead, pathArgs: []nativeToolPathArg{{name: "path", mode: toolPathRead, optional: true}}},
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (toolOutput, error) {
				return t.find(argString(args, "pattern"), argString(args, "path"), argInt(args, "max_matches"))
			},
		},
		{
			name:        "file_info",
			description: "Return file metadata such as type, byte size, mode, and line count for text files.",
			parameters:  obj([]string{"path"}, map[string]interface{}{"path": str("Workspace-relative path.")}),
			metadata:    nativeToolMetadata{sideEffect: toolRead, pathArgs: []nativeToolPathArg{{name: "path", mode: toolPathRead}}},
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (toolOutput, error) {
				return t.fileInfo(argString(args, "path"))
			},
		},
		{
			name:        "skill",
			description: "List available Telos skills, read one skill body, or read a referenced file inside a skill directory. Use this for required rubrics and task-specific skills.",
			parameters: obj([]string{"action"}, map[string]interface{}{
				"action":      enumString("Either 'list', 'read', or 'read_ref'.", "list", "read", "read_ref"),
				"name":        str("Skill name for action='read' or action='read_ref'."),
				"path":        str("Relative file path inside the skill directory for action='read_ref'."),
				"start_line":  integer("Optional 1-based start line for read/read_ref."),
				"limit_lines": integer("Optional maximum lines to return for read/read_ref."),
			}),
			metadata: nativeToolMetadata{sideEffect: toolRead},
			run: func(t *nativeTools, _ context.Context, args map[string]interface{}) (toolOutput, error) {
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
// tools. The gateway is OpenAI-compatible, so this is the only schema
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

func nativeToolsForProviderCore() []providercore.ToolDefinition {
	out := make([]providercore.ToolDefinition, 0, len(nativeToolDefs))
	for _, def := range nativeToolDefs {
		out = append(out, providercore.ToolDefinition{
			Name:        def.name,
			Description: def.description,
			Parameters:  def.parameters,
		})
	}
	return out
}
