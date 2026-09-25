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
	Controller      bool
	PrimarySpecPath string
	ReviewBudget    bool
	ReviewCycleCap  int
}

// RenderProverTask builds the full prover task prompt.
func RenderProverTask(compiled *CompiledEnvironment, transcriptPath string, opts ...PromptOptions) string {
	options := promptOptions(opts)
	preamble, _ := ReadPrompt("prover.md")
	if options.Controller {
		controller, _ := ReadPrompt("controller.md")
		preamble = joinNonEmpty([]string{controller, preamble})
	}
	parts := []string{
		preamble,
		renderSessionContext(compiled, options),
		renderSpec(compiled),
		renderSkillsRoster(compiled, RoleProver),
		renderTranscriptProtocol(transcriptPath),
		renderWorkspace(),
		renderOutputContract(RoleProver, options),
	}
	return joinNonEmpty(parts)
}

// RenderVerifierTask builds the full verifier task prompt.
func RenderVerifierTask(compiled *CompiledEnvironment, transcriptPath string, opts ...PromptOptions) string {
	options := promptOptions(opts)
	preamble, _ := ReadPrompt("verifier.md")
	parts := []string{
		preamble,
		renderSessionContext(compiled, options),
		renderSpec(compiled),
		renderSkillsRoster(compiled, RoleVerifier),
		renderTranscriptProtocol(transcriptPath),
		renderWorkspace(),
		renderOutputContract(RoleVerifier, options),
	}
	return joinNonEmpty(parts)
}

func promptOptions(opts []PromptOptions) PromptOptions {
	if len(opts) == 0 {
		return PromptOptions{}
	}
	return opts[0]
}

func renderSessionContext(compiled *CompiledEnvironment, opts PromptOptions) string {
	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	lines := []string{
		"## Session",
		"",
		fmt.Sprintf("- Spec: `%s`", compiled.Environment.Name),
		fmt.Sprintf("- Platform: `%s`", platform),
	}
	if opts.Controller {
		lines = append(lines, "- Session kind: `controller`")
	}
	if opts.PrimarySpecPath != "" {
		lines = append(lines, fmt.Sprintf("- Primary spec: `%s`", opts.PrimarySpecPath))
	}
	if opts.ReviewBudget && opts.ReviewCycleCap > 0 {
		lines = append(lines, fmt.Sprintf("- Review cycle cap: at most `%d` verifier cycles", opts.ReviewCycleCap))
	}
	if platform != "local" {
		lines = append(lines,
			fmt.Sprintf("- Namespace: `%s`", compiled.Namespace),
			"- The runtime supplies session identity and CLI credentials.",
		)
	}

	return strings.Join(lines, "\n")
}

func renderSpec(compiled *CompiledEnvironment) string {
	return "# Spec\n\n" + compiled.SpecText + "\n"
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
		desc := strings.TrimSpace(s.Description)
		marker := ""
		if requiredNames[s.Name] {
			marker = " - required evaluation rubric"
		}
		if desc != "" {
			lines = append(lines, fmt.Sprintf("- `%s`%s - %s", s.Name, marker, desc))
		} else {
			lines = append(lines, fmt.Sprintf("- `%s`%s", s.Name, marker))
		}
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
		"Read the transcript for current spec updates and unresolved findings before acting.",
		"On <external_update>, read the current spec and available diff named in the block before continuing.",
		"Reuse work that serves the current spec; remove behavior that only served superseded requirements. Preserve required data and history.",
		"Reassess earlier findings and approvals against the current spec and state.",
		"The runtime appends your response. Do not edit the transcript.",
	}, "\n")
}

func renderWorkspace() string {
	return "## Workspace\n\nDurable working tree; use git history to inspect prior work.\n" +
		"Child tasks use isolated workspaces. Inspect their transcripts and evidence; extract `workspace.tar.gz` checkpoints to integrate results, including git state.\n"
}

func renderOutputContract(role Role, opts PromptOptions) string {
	lines := []string{
		"## Output",
		"- Send brief <progress_update>...</progress_update> messages during meaningful changes, results, blockers, and long operations or waits.",
		"- Use everyday words and report only observed progress. Keep commands, file names, and test inventories in your report.",
		"- Finish with a concise Markdown report of changes, evidence, and remaining blockers, followed by a final <progress_update>...</progress_update> in the same response.",
	}
	if role == RoleProver {
		lines = append(lines, "- The final update states what is ready for independent review; do not claim independent verification.")
		return strings.Join(lines, "\n")
	}
	if opts.Controller {
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

func joinNonEmpty(parts []string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}
