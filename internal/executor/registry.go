package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type toolSideEffect string

const (
	toolRead    toolSideEffect = "read"
	toolMutate  toolSideEffect = "mutate"
	toolExecute toolSideEffect = "execute"
)

type toolPathMode string

const (
	toolPathRead  toolPathMode = "read"
	toolPathWrite toolPathMode = "write"
)

type nativeToolPathArg struct {
	name     string
	mode     toolPathMode
	optional bool
}

type nativeToolMetadata struct {
	sideEffect     toolSideEffect
	pathArgs       []nativeToolPathArg
	redactArgs     []string
	redactOutputs  []string
	changedFiles   bool
	previewOutputs bool
}

type nativeToolRegistry struct {
	byName map[string]nativeTool
}

func newNativeToolRegistry(defs []nativeTool) *nativeToolRegistry {
	r := &nativeToolRegistry{byName: map[string]nativeTool{}}
	for _, tool := range defs {
		r.byName[tool.name] = tool
		for _, alias := range tool.aliases {
			r.byName[alias] = tool
		}
	}
	return r
}

func (r *nativeToolRegistry) run(t *nativeTools, ctx context.Context, call nativeToolCall) nativeToolResult {
	if call.ID == "" {
		call.ID = call.Name
	}
	tool, ok := r.byName[call.Name]
	if !ok {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: fmt.Sprintf("unknown tool %q; available tools are %s", call.Name, oxfordList(nativeToolNames())), IsError: true, ErrorCode: errAgentProtocol}
	}
	args, err := parseToolArguments(call.Arguments)
	if err != nil {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: err.Error(), IsError: true, ErrorCode: errAgentProtocol}
	}
	if err := validateToolArgs(tool, args); err != nil {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: err.Error(), IsError: true, ErrorCode: errAgentProtocol}
	}
	if err := t.preflightToolPaths(tool, args); err != nil {
		return nativeToolResult{CallID: call.ID, Name: call.Name, Output: err.Error(), IsError: true, ErrorCode: errAgentProtocol, Metadata: t.envelopeMetadata(nil)}
	}

	started := time.Now()
	out, err := tool.run(t, ctx, args)
	durationMS := time.Since(started).Milliseconds()
	if err != nil {
		if out.fields == nil && len(out.bodies) == 0 {
			out = toolOutput{bodies: []toolBodySection{{Text: err.Error()}}}
		}
		t.attachContainmentField(&out)
		if out.errorCode == "" {
			out.errorCode = classifyToolError(err)
		}
		result := nativeToolResult{
			CallID:     call.ID,
			Name:       call.Name,
			Output:     renderToolOutput(call.Name, false, durationMS, out),
			IsError:    true,
			ErrorCode:  out.errorCode,
			DurationMS: durationMS,
		}
		applyToolOutput(&result, out)
		result.Metadata = t.envelopeMetadata(result.Metadata)
		return result
	}
	t.attachContainmentField(&out)
	result := nativeToolResult{
		CallID:     call.ID,
		Name:       call.Name,
		Output:     renderToolOutput(call.Name, true, durationMS, out),
		DurationMS: durationMS,
	}
	applyToolOutput(&result, out)
	result.Metadata = t.envelopeMetadata(result.Metadata)
	return result
}

func (t *nativeTools) attachContainmentField(out *toolOutput) {
	if t.containmentMode == "" {
		return
	}
	out.fields = append(out.fields, toolField{Key: "containment_mode", Value: string(t.containmentMode)})
}

func parseToolArguments(raw string) (map[string]interface{}, error) {
	args := map[string]interface{}{}
	if strings.TrimSpace(raw) == "" {
		return args, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("invalid tool arguments; resend a single JSON object: %v", err)
	}
	if args == nil {
		return nil, fmt.Errorf("invalid tool arguments; expected a JSON object")
	}
	offset := decoder.InputOffset()
	rest := ""
	if offset >= 0 && offset < int64(len(raw)) {
		rest = strings.TrimSpace(raw[offset:])
	}
	if rest == "" {
		return args, nil
	}
	var extra any
	second := json.NewDecoder(strings.NewReader(rest))
	second.UseNumber()
	if err := second.Decode(&extra); err == nil {
		return nil, fmt.Errorf("invalid tool arguments; resend exactly one JSON object")
	}
	return args, nil
}

func validateToolArgs(tool nativeTool, args map[string]interface{}) error {
	required := stringSetFromSchema(tool.parameters["required"])
	props, _ := tool.parameters["properties"].(map[string]interface{})
	for key := range required {
		if _, ok := args[key]; !ok {
			return fmt.Errorf("missing required argument %q", key)
		}
	}
	if additional, _ := tool.parameters["additionalProperties"].(bool); !additional {
		for key := range args {
			if _, ok := props[key]; !ok {
				return fmt.Errorf("unexpected argument %q", key)
			}
		}
	}
	for key, raw := range args {
		spec, ok := props[key].(map[string]interface{})
		if !ok || raw == nil {
			continue
		}
		if err := validateValueAgainstSchema(key, raw, spec); err != nil {
			return err
		}
	}
	return nil
}

func validateValueAgainstSchema(name string, value any, spec map[string]interface{}) error {
	want, _ := spec["type"].(string)
	if want != "" && !argMatchesType(value, want) {
		return fmt.Errorf("argument %q must be of type %s", name, want)
	}
	if enum, ok := spec["enum"].([]interface{}); ok && len(enum) > 0 {
		var matched bool
		for _, allowed := range enum {
			if fmt.Sprint(value) == fmt.Sprint(allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("argument %q must be one of %s", name, schemaEnumList(enum))
		}
	}
	if want == "object" {
		obj, _ := value.(map[string]interface{})
		props, _ := spec["properties"].(map[string]interface{})
		if additional, ok := spec["additionalProperties"]; ok {
			if allowed, ok := additional.(bool); ok && !allowed {
				for key := range obj {
					if _, ok := props[key]; !ok {
						return fmt.Errorf("argument %q has unexpected property %q", name, key)
					}
				}
			}
			if addSpec, ok := additional.(map[string]interface{}); ok {
				for key, raw := range obj {
					if err := validateValueAgainstSchema(name+"."+key, raw, addSpec); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func stringSetFromSchema(raw any) map[string]bool {
	out := map[string]bool{}
	switch values := raw.(type) {
	case []string:
		for _, value := range values {
			out[value] = true
		}
	case []interface{}:
		for _, value := range values {
			if text, ok := value.(string); ok {
				out[text] = true
			}
		}
	}
	return out
}

func schemaEnumList(enum []interface{}) string {
	parts := make([]string, 0, len(enum))
	for _, item := range enum {
		parts = append(parts, fmt.Sprintf("%q", item))
	}
	return strings.Join(parts, ", ")
}

func argMatchesType(value any, want string) bool {
	switch want {
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		return isIntegralNumber(value)
	case "number":
		return isJSONNumber(value)
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

func isIntegralNumber(value any) bool {
	switch v := value.(type) {
	case float64:
		return isFiniteNumber(v) && math.Trunc(v) == v
	case int:
		return true
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return true
		}
		return false
	default:
		return false
	}
}

func isJSONNumber(value any) bool {
	switch v := value.(type) {
	case float64:
		return isFiniteNumber(v)
	case int:
		return true
	case json.Number:
		f, err := v.Float64()
		return err == nil && isFiniteNumber(f)
	default:
		return false
	}
}

func isFiniteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (t *nativeTools) preflightToolPaths(tool nativeTool, args map[string]interface{}) error {
	for _, pathArg := range tool.metadata.pathArgs {
		value := argString(args, pathArg.name)
		if strings.TrimSpace(value) == "" {
			if pathArg.optional {
				continue
			}
			return fmt.Errorf("%s is required", pathArg.name)
		}
		var err error
		switch pathArg.mode {
		case toolPathWrite:
			_, err = t.resolveWritePath(value)
		default:
			_, err = t.resolvePath(value)
		}
		if err != nil {
			t.logOutsideWorkspaceAttempt(tool.name, value, pathArg.mode == toolPathWrite)
			return err
		}
	}
	return nil
}

func (t *nativeTools) envelopeMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["containment_mode"] = string(t.containmentMode)
	return metadata
}

func changedFilePreview(content string) string {
	const maxLines = 12
	lines := strings.Split(content, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "... preview truncated ...")
	}
	preview, _ := truncateText(strings.Join(lines, "\n"), 4096)
	return preview
}

func ensureNoStaleWrite(tracker *fileTracker, full, rel string) error {
	err := tracker.check(full)
	if err == nil {
		return nil
	}
	if errors.Is(err, errFileChangedOnDisk) {
		return staleWriteError(rel)
	}
	return err
}

func compactDiffPreview(oldText, newText string) string {
	if oldText == newText {
		return "no content changes"
	}
	var buf bytes.Buffer
	buf.WriteString("before:\n")
	buf.WriteString(changedFilePreview(oldText))
	buf.WriteString("\nafter:\n")
	buf.WriteString(changedFilePreview(newText))
	return buf.String()
}
