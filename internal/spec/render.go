package spec

import (
	"fmt"
	"strings"
)

// Role is the PVG agent role.
type Role = string

const (
	RoleProver   Role = "prover"
	RoleVerifier Role = "verifier"
)

// RenderProverTask builds the full prover task prompt.
func RenderProverTask(compiled *CompiledEnvironment, roundNum int, workspace, transcript string) string {
	preamble, _ := ReadPrompt("prover.md")
	parts := []string{
		sessionTitle(compiled.Environment.Name, RoleProver, roundNum),
		preamble,
		"",
		renderPlatformPreamble(compiled),
		renderSessionContext(compiled, RoleProver, roundNum),
		renderRequirements(compiled),
		renderRequiredVerificationCriteria(compiled, RoleProver),
		renderSkillsRoster(compiled),
		renderTranscript(transcript),
		renderWorkspace(workspace, RoleProver),
		renderOutputContract(RoleProver),
	}
	return joinNonEmpty(parts)
}

// RenderVerifierTask builds the full verifier task prompt.
func RenderVerifierTask(compiled *CompiledEnvironment, workspace, transcript string) string {
	preamble, _ := ReadPrompt("verifier.md")
	parts := []string{
		sessionTitle(compiled.Environment.Name, RoleVerifier, 0),
		preamble,
		"",
		renderPlatformPreamble(compiled),
		renderSessionContext(compiled, RoleVerifier, 0),
		renderRequirements(compiled),
		renderRequiredVerificationCriteria(compiled, RoleVerifier),
		renderSkillsRoster(compiled),
		renderTranscript(transcript),
		renderWorkspace(workspace, RoleVerifier),
		renderOutputContract(RoleVerifier),
	}
	return joinNonEmpty(parts)
}

func sessionTitle(name string, role Role, roundNum int) string {
	if role == RoleVerifier {
		return fmt.Sprintf("# Verify: %s\n", name)
	}
	if roundNum <= 1 {
		return fmt.Sprintf("# Build: %s\n", name)
	}
	return fmt.Sprintf("# Fix: %s\n", name)
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

func renderSessionContext(compiled *CompiledEnvironment, role Role, roundNum int) string {
	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	lines := []string{"## Session\n"}
	if platform != "local" {
		lines = append(lines, fmt.Sprintf("- Namespace: `%s`", compiled.Namespace))
	}

	if role == RoleProver {
		if roundNum <= 1 {
			lines = append(lines, "",
				"### Objective",
				"- satisfy the system spec from the current starting state",
				"",
			)
		} else {
			lines = append(lines, "",
				"### Objective",
				"- address concrete verifier findings from the PVG transcript while preserving the live system",
				"",
				"### Constraints",
				"- do not solve the problem by wiping system state unless the spec explicitly allows it",
				"",
			)
		}
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

func renderRequirements(compiled *CompiledEnvironment) string {
	return "## Requirements\n\n" + compiled.SpecText + "\n"
}

func renderRequiredVerificationCriteria(compiled *CompiledEnvironment, role Role) string {
	if len(compiled.RequiredVerifierSkills) == 0 {
		return ""
	}
	lines := []string{"## Required Verification Criteria", ""}
	if role == RoleProver {
		lines = append(lines,
			"The verifier will grade the delivered work against these starred skills. Treat them as part of the contract, not as optional style advice.",
			"",
		)
		for _, s := range compiled.RequiredVerifierSkills {
			desc := strings.TrimSpace(s.Description)
			entry := fmt.Sprintf("- `%s`", s.Name)
			if desc != "" {
				entry += " - " + desc
			}
			lines = append(lines, entry)
		}
		lines = append(lines, "")
		return strings.Join(lines, "\n")
	}

	// verifier
	lines = append(lines,
		"The following starred skills are mandatory grading rubrics. Apply each one explicitly before conceding.",
		"",
		"For each required criterion:",
		"- state PASS or FAIL;",
		"- cite concrete artifact, source, tree, or runtime evidence;",
		"- raise a blocking finding for any failed criterion;",
		"- use <status>CONTINUE</status> if any criterion fails;",
		"- do not concede unless every required criterion passes.",
		"",
		"A required rubric can block concession even when the narrow functional contract appears satisfied. That is intentional: required criteria are part of the game contract.",
		"",
	)
	for _, s := range compiled.RequiredVerifierSkills {
		lines = append(lines,
			fmt.Sprintf("### %s", s.Name),
			"",
			strings.TrimSpace(s.Instructions),
			"",
		)
	}
	return strings.Join(lines, "\n")
}

func renderSkillsRoster(compiled *CompiledEnvironment) string {
	if len(compiled.Skills) == 0 {
		return ""
	}
	requiredNames := map[string]bool{}
	for _, s := range compiled.RequiredVerifierSkills {
		requiredNames[s.Name] = true
	}
	lines := []string{
		"## Skills",
		"",
		"Use these skill descriptions as routing hints. If the runtime exposes matching skill files, read them before acting. Skills marked `required verifier criterion` are grading rubrics, not optional guidance.",
		"",
	}
	for _, s := range compiled.Skills {
		desc := strings.TrimSpace(s.Description)
		marker := ""
		if requiredNames[s.Name] {
			marker = " - required verifier criterion"
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

func renderTranscript(transcript string) string {
	if transcript == "" {
		return ""
	}
	return strings.Join([]string{
		"## PVG Transcript",
		"",
		"This append-only transcript is the game control file and background-agent product surface. Use it to understand prior prover claims, verifier reviews, progress updates, and unresolved findings; do not treat it as live system state.",
		"",
		"~~~~markdown",
		strings.TrimSpace(transcript),
		"~~~~",
		"",
	}, "\n")
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
			"You may **reset** the prover's commits if they introduced regressions: `git reset --soft HEAD~N`\n",
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
			"- Write concise Markdown for the PVG transcript with claims, evidence, changes made, and remaining uncertainty",
			"- During the turn, emit concise <progress_update>...</progress_update> entries when useful for a background observer, without spamming routine tool activity",
			"- End every turn with one final <progress_update>what you did this round</progress_update>",
		}, "\n")
	}
	return strings.Join([]string{
		"## Output",
		"- Write concise Markdown for the PVG transcript; blocking findings first",
		"- During the turn, emit concise <progress_update>...</progress_update> entries when useful for a background observer, without spamming routine tool activity",
		"- End every turn with one final <progress_update>what you found or why you concede</progress_update>",
		"- The final non-empty line must be exactly one status tag",
		"- <status>CONTINUE</status> if you found a concrete contract violation",
		"- <status>CONTINUE</status> if any required task is pending, running, stopped, failed, or not reflected in live resources",
		`- Include an "Artifact Hygiene" section for code-producing tasks: tree shape inspected, notable debt, and whether it blocks concession`,
		`- If "Required Verification Criteria" are present, include a "Required Criteria Applied" section with PASS/FAIL and evidence for each required criterion`,
		"- If any required criterion is FAIL, the final status must be <status>CONTINUE</status>",
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
