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
}

// RenderProverTask builds the full prover task prompt.
func RenderProverTask(compiled *CompiledEnvironment, workspace, transcriptPath string, opts ...PromptOptions) string {
	options := promptOptions(opts)
	preamble, _ := ReadPrompt("prover.md")
	if options.Controller {
		controller, _ := ReadPrompt("controller.md")
		preamble = joinNonEmpty([]string{controller, "", preamble})
	}
	parts := []string{
		preamble,
		"",
		renderPlatformPreamble(compiled),
		renderSessionContext(compiled, RoleProver, options),
		renderSpec(compiled),
		renderRequiredEvaluationRubrics(compiled, RoleProver),
		renderSkillsRoster(compiled, options),
		renderTranscriptProtocol(transcriptPath, RoleProver),
		renderWorkspace(workspace, RoleProver),
		renderOutputContract(RoleProver),
	}
	return joinNonEmpty(parts)
}

// RenderVerifierTask builds the full verifier task prompt.
func RenderVerifierTask(compiled *CompiledEnvironment, workspace, transcriptPath string, opts ...PromptOptions) string {
	options := promptOptions(opts)
	preamble, _ := ReadPrompt("verifier.md")
	parts := []string{
		preamble,
		"",
		renderPlatformPreamble(compiled),
		renderSessionContext(compiled, RoleVerifier, options),
		renderSpec(compiled),
		renderRequiredEvaluationRubrics(compiled, RoleVerifier),
		renderSkillsRoster(compiled, options),
		renderTranscriptProtocol(transcriptPath, RoleVerifier),
		renderWorkspace(workspace, RoleVerifier),
		renderOutputContract(RoleVerifier),
	}
	return joinNonEmpty(parts)
}

func promptOptions(opts []PromptOptions) PromptOptions {
	if len(opts) == 0 {
		return PromptOptions{}
	}
	return opts[0]
}

func renderPlatformPreamble(compiled *CompiledEnvironment) string {
	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	text, err := ReadPrompt("preamble/" + platform + ".md")
	if err != nil {
		return ""
	}
	return text
}

func renderSessionContext(compiled *CompiledEnvironment, role Role, opts PromptOptions) string {
	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	lines := []string{
		"## Session",
		"",
		fmt.Sprintf("- Spec: `%s`", compiled.Environment.Name),
		fmt.Sprintf("- Role: `%s`", displayRole(role)),
	}
	if opts.Controller {
		lines = append(lines, "- Session kind: `controller`")
	}
	if opts.PrimarySpecPath != "" {
		lines = append(lines, fmt.Sprintf("- Primary spec: `%s`", opts.PrimarySpecPath))
	}
	if platform != "local" {
		lines = append(lines, fmt.Sprintf("- Namespace: `%s`", compiled.Namespace))
	}

	if role == RoleProver {
		lines = append(lines, "",
			"### Operating Posture",
			"- continue from the append-only transcript, workspace, and live environment",
			"- if unresolved evaluator findings exist, address them before broadening the work",
			"- otherwise advance the delivered system toward the contract",
			"- preserve valid existing work and live state unless the spec explicitly allows replacement",
			"",
		)
	} else {
		lines = append(lines, "",
			"### Verification Focus",
			"- judge the delivered artifact against the spec and applicable quality bars",
			"- inspect source, tree state, generated artifacts, and runtime behavior as needed",
			"- run checks when behavior is load-bearing or unclear; do not probe blindly",
			"- produce concrete findings for contract violations or blocking maintainability debt",
			"",
			"### Constraints",
			"- do not invent new requirements",
			"",
		)
	}
	return strings.Join(lines, "\n")
}

func renderSpec(compiled *CompiledEnvironment) string {
	return "# Spec\n\n" + compiled.SpecText + "\n"
}

func displayRole(role Role) string {
	if role == RoleVerifier {
		return "evaluation"
	}
	return "implementation"
}

func renderRequiredEvaluationRubrics(compiled *CompiledEnvironment, role Role) string {
	if len(compiled.RequiredVerifierSkills) == 0 {
		return ""
	}
	lines := []string{"## Required Evaluation Rubrics", ""}
	if role == RoleProver {
		lines = append(lines,
			"The evaluator will load these starred skills by name and use them as grading rubrics. Treat each named rubric as part of the contract, not optional style advice.",
			"",
		)
		lines = appendSkillPointers(lines, compiled.RequiredVerifierSkills)
		return strings.Join(lines, "\n")
	}

	lines = append(lines,
		"The following starred skills are mandatory grading rubrics. Use each mounted skill by name before conceding.",
		"",
		"For each required rubric skill:",
		"- state PASS or FAIL;",
		"- cite concrete artifact, source, tree, or runtime evidence;",
		"- raise a blocking finding for any failed rubric;",
		"- use <status>CONTINUE</status> if any rubric fails;",
		"- do not concede unless every required rubric passes.",
		"",
		"A required rubric can block concession even when the narrow functional contract appears satisfied. That is intentional: required rubrics are part of the session contract.",
		"",
	)
	lines = appendSkillPointers(lines, compiled.RequiredVerifierSkills)
	return strings.Join(lines, "\n")
}

func renderSkillsRoster(compiled *CompiledEnvironment, opts PromptOptions) string {
	skills := effectiveSkills(compiled, opts)
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
		"Use these skill names as routing hints. Pi can load mounted skill files by name; the prompt references names rather than inlining skill bodies. Skills marked `required evaluation rubric` are grading rubrics, not optional guidance.",
		"",
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

func appendSkillPointers(lines []string, skills []*Skill) []string {
	for _, s := range skills {
		desc := strings.TrimSpace(s.Description)
		entry := fmt.Sprintf("- `%s`", s.Name)
		if desc != "" {
			entry += " - " + desc
		}
		lines = append(lines, entry)
	}
	lines = append(lines, "")
	return lines
}

func effectiveSkills(compiled *CompiledEnvironment, opts PromptOptions) []*Skill {
	skills := append([]*Skill{}, compiled.Skills...)
	if !opts.Controller || hasSkill(skills, "telos-orchestrate") {
		return skills
	}
	controllerSkill := ResolveBuiltinSkill("telos-orchestrate")
	if controllerSkill == nil {
		controllerSkill = &Skill{
			Name:        "telos-orchestrate",
			Description: "Telos controller runtime.",
		}
	}
	return append(skills, controllerSkill)
}

func hasSkill(skills []*Skill, name string) bool {
	for _, s := range skills {
		if s.Name == name {
			return true
		}
	}
	return false
}

func renderTranscriptProtocol(transcriptPath string, role Role) string {
	transcriptPath = strings.TrimSpace(transcriptPath)
	if transcriptPath == "" {
		return ""
	}
	lines := []string{
		"## Transcript",
		"",
		fmt.Sprintf("- Path: `%s`", transcriptPath),
		"- This is the append-only communication log between the implementation agent, evaluator, controller, and operators.",
		"- The runtime appends your assistant response to this file after the turn.",
		"- First action every turn: read this transcript path.",
		"- Use it to gather summarized session state: prior claims, delivered changes, evaluator findings, progress updates, and open uncertainty.",
		"- If the transcript only contains the header, proceed from scratch against the spec.",
		"- Do not paste, summarize, rewrite, or edit the whole transcript directly.",
		"- Write notes, claims, checks, findings, and uncertainty in your final response when they would help an independent evaluator.",
	}
	if role == RoleProver {
		lines = append(lines,
			"- Before making changes, identify unresolved evaluator findings and decide whether this turn is a fresh implementation or a repair.",
		)
	} else {
		lines = append(lines,
			"- Before judging, identify the implementation claims and any unresolved findings from prior evaluation turns.",
		)
	}
	return strings.Join(lines, "\n")
}

func renderWorkspace(workspace string, role Role) string {
	if workspace == "" {
		return ""
	}
	lines := []string{
		"\n## Workspace",
		"Files at `/workspace/output` persist across rounds.",
		"Use `git log` and `git diff` to see previous work.\n",
	}
	if role == RoleProver {
		lines = append(lines,
			"**Commit your work** after each meaningful change: `git add -A && git commit -m '<description>'`\n",
		)
	}
	if role == RoleVerifier {
		lines = append(lines,
			"You may **reset** implementation commits if they introduced regressions: `git reset --soft HEAD~N`\n",
			"Use this snapshot as evidence of delivered tree shape: changed files, untracked artifacts, generated outputs, and diff size may matter for artifact hygiene.\n",
		)
	}
	lines = append(lines, fmt.Sprintf("```\n%s\n```\n", workspace))
	return strings.Join(lines, "\n")
}

func renderOutputContract(role Role) string {
	if role == RoleProver {
		return strings.Join([]string{
			"## Output",
			"- Your assistant response is appended to the transcript automatically; do not write to `/dev/stdout` or edit the transcript file directly",
			"- Do not add a duplicate turn heading; the runtime writes turn headings and metadata",
			"- Write concise Markdown with claims, evidence, changes made, and remaining uncertainty",
			"- During the turn, emit concise <progress_update>...</progress_update> entries when useful for a background observer, without spamming routine tool activity",
			"- End every turn with one final <progress_update>what you did this round</progress_update>",
		}, "\n")
	}
	return strings.Join([]string{
		"## Output",
		"- Your assistant response is appended to the transcript automatically; do not write to `/dev/stdout` or edit the transcript file directly",
		"- Do not add a duplicate turn heading; the runtime writes turn headings and metadata",
		"- Write concise Markdown; blocking findings first",
		"- During the turn, emit concise <progress_update>...</progress_update> entries when useful for a background observer, without spamming routine tool activity",
		"- End every turn with one final <progress_update>what you found or why you concede</progress_update>",
		"- The final non-empty line must be exactly one status tag",
		"- <status>CONTINUE</status> if you found a concrete contract violation",
		"- <status>CONTINUE</status> if any required task is pending, running, stopped, failed, or not reflected in live resources",
		`- Include an "Artifact Hygiene" section for code-producing tasks: tree shape inspected, notable debt, and whether it blocks concession`,
		`- If "Required Evaluation Rubrics" are present, include a "Required Rubrics Applied" section with PASS/FAIL and evidence for each required rubric`,
		"- If any required rubric is FAIL, the final status must be <status>CONTINUE</status>",
		"- <status>CONCEDE</status> only if the contract and applicable quality bars hold under independent review",
	}, "\n")
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
