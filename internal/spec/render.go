package spec

import (
	"fmt"
	"strings"
)

// Role is the internal agent role.
type Role = string

const (
	RoleProver   Role = "prover"
	RoleVerifier Role = "verifier"
)

// PromptOptions carries session metadata that affects prompt rendering.
type PromptOptions struct {
	Persistent      bool
	PrimarySpecPath string
	ReviewCycleCap  int
	// LocalRuntime marks a session that executes on a workstation. Sessions
	// otherwise execute in Telos Cloud.
	LocalRuntime bool
}

// RenderTask builds the complete task prompt for one agent role.
func RenderTask(role Role, compiled *CompiledEnvironment, transcriptPath string, opts ...PromptOptions) string {
	options := PromptOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	rolePrompt, _ := ReadPrompt(role + ".md")
	var parts []string
	for _, part := range []string{
		rolePrompt,
		renderSessionContext(compiled, options),
		"# Spec\n\n" + compiled.SpecText + "\n",
		renderSkillsRoster(compiled, role),
		renderTranscriptProtocol(transcriptPath),
		renderOutputContract(role, options),
	} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n")
}

func renderSessionContext(compiled *CompiledEnvironment, opts PromptOptions) string {
	runtime := "cloud"
	if opts.LocalRuntime {
		runtime = "local"
	}
	lifecycle := "bounded"
	if opts.Persistent {
		lifecycle = "persistent"
	}
	lines := []string{
		"## Session",
		"",
		fmt.Sprintf("- Spec: `%s`", compiled.Environment.Name),
		fmt.Sprintf("- Runtime: `%s`", runtime),
		fmt.Sprintf("- Lifecycle: `%s`", lifecycle),
		"- Workspace: durable working tree.",
		"- Root goal changes (`telos apply`) are reserved for operators.",
	}
	if opts.Persistent {
		lines = append(lines, "- If the spec already holds, preserve the working state and report the evidence.")
	}
	if opts.PrimarySpecPath != "" {
		lines = append(lines, fmt.Sprintf("- Primary spec: `%s`", opts.PrimarySpecPath))
	}
	if opts.ReviewCycleCap > 0 {
		lines = append(lines, fmt.Sprintf("- Review cycle cap: at most `%d` verifier cycles", opts.ReviewCycleCap))
	}
	if !opts.LocalRuntime {
		lines = append(lines, fmt.Sprintf("- Namespace: `%s`", compiled.Namespace))
	}

	return strings.Join(lines, "\n")
}

func renderSkillsRoster(compiled *CompiledEnvironment, role Role) string {
	skills := compiled.Skills
	if len(skills) == 0 {
		return ""
	}
	requiredNames := map[string]bool{}
	for _, s := range compiled.RequiredVerifierSkills {
		requiredNames[s.Name] = true
	}
	lines := []string{
		"## Skills",
		"",
		"Skills marked `required evaluation rubric` are part of the goal.",
		"",
	}
	if role == RoleVerifier && len(requiredNames) > 0 {
		lines = append(lines, "Load every required rubric and report PASS or FAIL with evidence for each. Any failure blocks concession.", "")
	}
	for _, s := range skills {
		entry := fmt.Sprintf("- `%s`", s.Name)
		if requiredNames[s.Name] {
			entry += " - required evaluation rubric"
		}
		if desc := strings.TrimSpace(s.Description); desc != "" {
			entry += " - " + desc
		}
		lines = append(lines, entry)
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}

func renderTranscriptProtocol(transcriptPath string) string {
	transcriptPath = strings.TrimSpace(transcriptPath)
	if transcriptPath == "" {
		return ""
	}
	return strings.Join([]string{
		"## Transcript",
		fmt.Sprintf("Path: `%s`", transcriptPath),
		"Read recent transcript entries and account for unresolved findings before acting. Consult older history when needed.",
		"On <external_update>, read the current spec and available diff before continuing.",
		"Only the current spec governs; preserve required data and history.",
		"The runtime appends your response; do not edit the transcript.",
	}, "\n")
}

func renderOutputContract(role Role, opts PromptOptions) string {
	lines := []string{
		"## Output and progress",
		"- Keep evidence, technical details, and uncertainty in your report. Report only what you established.",
		"- Use <progress_update>...</progress_update> for short updates in everyday language when work begins, a result is established, direction changes, or a blocker appears.",
		"- During waits, report only observed activity.",
		"- Finish with your report and one final <progress_update>...</progress_update> in the same response; do not send a separate update-only final response.",
	}
	if role == RoleProver {
		lines = append(lines, "- The final update states what is ready for independent review; do not claim independent verification.")
		return strings.Join(lines, "\n")
	}
	if opts.Persistent {
		lines = append(lines,
			"- Concede this cycle when the goal holds, or the only next action is waiting for an inspected pending/running child and no duplicate work was launched. Waiting does not mean the goal is complete.",
			"- Relevant failed or stopped children, and completed children with uninspected or missing expected results, are blockers.",
		)
	} else {
		lines = append(lines, "- Concede only when every obligation holds under independent review.")
	}
	lines = append(lines,
		"- Put blockers first; the final update states what you independently confirmed or what still blocks progress.",
		"- End with exactly one status tag on its own final line: <status>CONCEDE</status> to concede, otherwise <status>CONTINUE</status>.",
	)
	return strings.Join(lines, "\n")
}
