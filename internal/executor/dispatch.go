package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

type nativeTools struct {
	platform        *platform.LocalPlatform
	stopRequested   func() bool
	registry        *nativeToolRegistry
	scope           *workspaceScope
	fileTracker     *fileTracker
	containmentMode ContainmentMode
	limit           toolOutputLimit
	skills          map[string]nativeSkillRef
	// skillCoverage records, per skill name, the number of leading lines read
	// contiguously from line 1. A required rubric counts as fully read once this
	// reaches its line count, so paginated reads (start_line walking to EOF)
	// satisfy the read gate just like a single complete read.
	skillCoverage map[string]int
	openedSkills  map[string]bool
	logger        *nativeSessionLogger
	budget        game.TurnBudget
}

type nativeToolsOption func(*nativeTools)

func withNativeToolsContainment(mode ContainmentMode) nativeToolsOption {
	return func(t *nativeTools) {
		t.containmentMode = mode
	}
}

func newNativeTools(p *platform.LocalPlatform, stopRequested func() bool, skills []game.TurnSkill, logger *nativeSessionLogger, knobs envKnobs, budget game.TurnBudget, opts ...nativeToolsOption) *nativeTools {
	scope, _ := newWorkspaceScope(p.Workspace)
	t := &nativeTools{
		platform:        p,
		stopRequested:   stopRequested,
		registry:        newNativeToolRegistry(nativeToolDefs),
		scope:           scope,
		fileTracker:     newFileTracker(),
		containmentMode: ContainmentUncontained,
		limit:           defaultToolOutputLimit(knobs),
		skills:          skillRefsFromTurn(skills),
		skillCoverage:   map[string]int{},
		openedSkills:    map[string]bool{},
		logger:          logger,
		budget:          budget,
	}
	for _, opt := range opts {
		opt(t)
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
	return t.registry.run(t, ctx, call)
}

// applyToolOutput populates the Metadata, ExitCode, HasExitCode, and Truncated
// fields of a nativeToolResult directly from the structured toolOutput — no
// re-parsing of the rendered text.
func applyToolOutput(r *nativeToolResult, out toolOutput) {
	for _, f := range out.fields {
		if r.Metadata == nil {
			r.Metadata = map[string]any{}
		}
		r.Metadata[f.Key] = f.Value
	}
	if out.exitCode != nil {
		r.ExitCode = *out.exitCode
		r.HasExitCode = true
	}
	if len(out.changedFiles) > 0 {
		if r.Metadata == nil {
			r.Metadata = map[string]any{}
		}
		r.Metadata["changed_files"] = append([]string(nil), out.changedFiles...)
	}
	if out.preview != "" {
		if r.Metadata == nil {
			r.Metadata = map[string]any{}
		}
		r.Metadata["preview"] = out.preview
	}
	r.Truncated = r.Truncated || out.truncated
}

// classifyToolError infers the error code from an error returned by a tool
// handler that did not set one explicitly. Handlers that know their error
// code set it on toolOutput.errorCode directly.
func classifyToolError(err error) executorErrorCode {
	if err == nil {
		return ""
	}
	if isToolPolicyDenial(err) {
		return errToolPolicyDenied
	}
	var failure toolFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	lower := strings.ToLower(err.Error())
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

func isToolPolicyDenial(err error) bool {
	if err == nil {
		return false
	}
	var denied policyDeniedError
	if errors.As(err, &denied) {
		return true
	}
	return errors.Is(err, os.ErrPermission)
}

type toolFailure struct {
	code   executorErrorCode
	reason string
}

func (e toolFailure) Error() string {
	return e.reason
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
		if isFiniteNumber(value) && math.Trunc(value) == value {
			return int(value)
		}
		return 0
	case int:
		return value
	case json.Number:
		if i, err := value.Int64(); err == nil {
			return int(i)
		}
		return 0
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
