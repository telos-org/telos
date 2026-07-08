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
		renderRequiredEvaluationRubrics(compiled, RoleProver, options),
		renderSkillsRoster(compiled, RoleProver, options),
		renderTranscriptProtocol(transcriptPath, RoleProver),
		renderWorkspace(workspace, RoleProver),
		renderOutputContract(RoleProver, options),
	}
	return joinNonEmpty(parts)
}

// RenderVerifierTask builds the full verifier task prompt.
func RenderVerifierTask(compiled *CompiledEnvironment, workspace, transcriptPath string, opts ...PromptOptions) string {
	options := promptOptions(opts)
	preamble := renderVerifierPreamble(options)
	parts := []string{
		preamble,
		"",
		renderPlatformPreamble(compiled),
		renderSessionContext(compiled, RoleVerifier, options),
		renderSpec(compiled),
		renderRequiredEvaluationRubrics(compiled, RoleVerifier, options),
		renderSkillsRoster(compiled, RoleVerifier, options),
		renderTranscriptProtocol(transcriptPath, RoleVerifier),
		renderWorkspace(workspace, RoleVerifier),
		renderOutputContract(RoleVerifier, options),
	}
	return joinNonEmpty(parts)
}

func renderVerifierPreamble(options PromptOptions) string {
	preamble, _ := ReadPrompt("verifier.md")
	return preamble
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
	if opts.ReviewBudget && opts.ReviewCycleCap > 0 {
		lines = append(lines, fmt.Sprintf("- Review cycle cap: at most `%d` verifier cycles", opts.ReviewCycleCap))
	}
	if platform != "local" {
		lines = append(lines, fmt.Sprintf("- Namespace: `%s`", compiled.Namespace))
	}

	if role == RoleProver {
		lines = append(lines, "",
			"### Operating Posture",
			"- continue from the append-only transcript, workspace, and live environment",
			"- if unresolved evaluator findings exist, address them before broadening the work",
			"- if the evaluator says no implementation change is recommended, preserve the current shape and revalidate tests, tree state, and named invariants only",
			"- otherwise make the smallest change that improves the delivered system against the goal",
			"- preserve valid existing work and live state unless the spec explicitly allows replacement",
			"",
		)
	} else {
		lines = append(lines, "",
			"### Verification Focus",
			"- judge the delivered artifact against the spec and applicable quality bars",
			"- inspect source, tree state, generated artifacts, and runtime behavior as needed",
			"- run checks when behavior is load-bearing or unclear; do not probe blindly",
			"- produce concrete findings for goal violations or blocking maintainability debt",
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

func renderRequiredEvaluationRubrics(compiled *CompiledEnvironment, role Role, opts PromptOptions) string {
	if len(compiled.RequiredVerifierSkills) == 0 {
		return ""
	}
	lines := []string{"## Required Evaluation Rubrics", ""}
	if role == RoleProver {
		lines = append(lines,
			"The evaluator will load these starred skills by name and use them as grading rubrics. Treat each named rubric as part of the goal, not optional style advice.",
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
		"A required rubric can block concession even when the surface behavior appears satisfied. That is intentional: required rubrics are part of what the session must deliver.",
		"",
	)
	lines = appendSkillPointers(lines, compiled.RequiredVerifierSkills)
	return strings.Join(lines, "\n")
}

func renderSkillsRoster(compiled *CompiledEnvironment, role Role, opts PromptOptions) string {
	skills := effectiveSkills(compiled, role, opts)
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
		"Use skill names as routing hints. The agent can load mounted skill files by name; prompts reference names instead of inlining skill bodies. Skills marked `required evaluation rubric` are grading rubrics, not optional guidance.",
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

func effectiveSkills(compiled *CompiledEnvironment, role Role, opts PromptOptions) []*Skill {
	skills := append([]*Skill{}, compiled.Skills...)
	if role == RoleProver {
		skills = implementationSkills(skills, compiled.RequiredVerifierSkills)
	}
	return skills
}

func implementationSkills(skills []*Skill, requiredVerifierSkills []*Skill) []*Skill {
	required := map[string]bool{}
	for _, skill := range requiredVerifierSkills {
		required[skill.Name] = true
	}
	defaultVerifier := map[string]bool{}
	for _, name := range DefaultVerifierSkills {
		defaultVerifier[name] = true
	}
	filtered := make([]*Skill, 0, len(skills))
	for _, skill := range skills {
		if defaultVerifier[skill.Name] && !required[skill.Name] {
			continue
		}
		filtered = append(filtered, skill)
	}
	return filtered
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
		"- Treat <external_update> blocks as operator/runtime changes to the desired spec; reload the current spec path named in the block and realign before continuing.",
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
		"The workspace below is the durable working tree for this session.",
		"Use `git log` and `git diff` to see previous work.\n",
	}
	if role == RoleProver {
		lines = append(lines,
			"**Commit your work** after each meaningful change: `git add -A && git commit -m '<description>'`\n",
		)
	}
	if role == RoleVerifier {
		lines = append(lines,
			"Do not rewrite, reset, or clean up implementation commits. Your job is to judge the delivered tree and report findings.\n",
			"Use this snapshot as evidence of delivered tree shape: changed files, untracked artifacts, generated outputs, and diff size may matter for artifact hygiene.\n",
			"Keep throwaway evaluator scratch outside the delivered tree. If a check becomes a reusable test, probe, fixture, or reproduction script, write it into the workspace in the natural test location or a small `evaluation/` directory so future turns can run it again.\n",
		)
	}
	lines = append(lines, fmt.Sprintf("```\n%s\n```\n", workspace))
	return strings.Join(lines, "\n")
}

func renderOutputContract(role Role, opts PromptOptions) string {
	if role == RoleProver {
		return strings.Join([]string{
			"## Output",
			"- Your assistant response is appended to the transcript automatically; do not write to `/dev/stdout` or edit the transcript file directly",
			"- Do not add a duplicate turn heading; the runtime writes turn headings and metadata",
			"- Write concise Markdown with claims, evidence, changes made, and remaining uncertainty",
			"- Emit concise <progress_update>...</progress_update> entries for a background observer at meaningful milestones: after planning, before and after long deploys, waits, probes, repairs, and verification passes",
			"- Before any operation expected to take more than 60 seconds, emit a progress update explaining what you are about to wait on or verify",
			"- During long-running work, keep progress updates regular enough that an observer sees useful movement about once per minute; do not save all progress for the final response",
			"- End every turn with one final <progress_update>what you did this round</progress_update>",
		}, "\n")
	}
	lines := []string{
		"## Output",
		"- Your assistant response is appended to the transcript automatically; do not write to `/dev/stdout` or edit the transcript file directly",
		"- Do not add a duplicate turn heading; the runtime writes turn headings and metadata",
		"- Write concise Markdown; blocking findings first",
		"- Emit concise <progress_update>...</progress_update> entries for a background observer at meaningful milestones: after scoping, before and after long probes, reproductions, waits, and verification passes",
		"- Before any operation expected to take more than 60 seconds, emit a progress update explaining what you are about to wait on or verify",
		"- During long-running evaluation, keep progress updates regular enough that an observer sees useful movement about once per minute; do not save all progress for the final response",
		"- End every turn with one final <progress_update>what you found or why you concede</progress_update>",
		"- The final non-empty line must be exactly one status tag",
		"- <status>CONTINUE</status> if you found a concrete goal violation",
	}
	if opts.Controller {
		lines = append(lines,
			"- For controller cycles, a pending or running child task is valid waiting work when the controller observed it first, launched no competing work, and did not claim final goal satisfaction",
			"- <status>CONCEDE</status> for that cycle if the correct next controller action is simply to wait for the child task",
			"- <status>CONTINUE</status> if a child is stopped, failed, terminal but uninspected, missing expected artifacts, or if the controller treats a launched/running task as final goal satisfaction",
		)
	}
	lines = append(lines,
		`- Include an "Artifact Hygiene" section for code-producing tasks: tree shape inspected, notable debt, and whether it blocks concession`,
		`- If "Required Evaluation Rubrics" are present, include a "Required Rubrics Applied" section with PASS/FAIL and evidence for each required rubric`,
		"- If any required rubric is FAIL, the final status must be <status>CONTINUE</status>",
		"- <status>CONCEDE</status> only if the goal and applicable quality bars hold under independent review",
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
