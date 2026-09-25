package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileEnvironment(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: compile-test\nplatform: local\n---\n# Compile Test\n\nTest body."), 0o644)

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	if compiled.Environment.Name != "compile-test" {
		t.Errorf("name: got %q", compiled.Environment.Name)
	}
	if compiled.Namespace != "ns-compile-test" {
		t.Errorf("namespace: got %q", compiled.Namespace)
	}
	if compiled.Cluster != "telos" {
		t.Errorf("cluster: got %q", compiled.Cluster)
	}
	if compiled.ContentHash == "" {
		t.Error("content hash should not be empty")
	}
	if len(compiled.ContentHash) != 16 {
		t.Errorf("content hash should be 16 chars, got %d", len(compiled.ContentHash))
	}
}

func TestCompileWithSkills(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Test skill\n---\nInstructions"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: skill-compile\nplatform: local\nskills:\n  - my-skill\n---\nBody"), 0o644)

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	found := false
	for _, s := range compiled.Skills {
		if s.Name == "my-skill" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected my-skill in compiled skills")
	}
}

func TestCompileEnvironmentWithBaseResolvesRelativeSkillsAgainstOverride(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(srcDir, "skills", "rel-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: rel-skill\ndescription: Relative\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	specBody := "---\nversion: 0.1.0\nname: rel-base-test\nplatform: local\nskills:\n  - skills/rel-skill\n---\nBody"
	srcSpec := filepath.Join(srcDir, "SPEC.md")
	if err := os.WriteFile(srcSpec, []byte(specBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// Copy the spec into a separate dir without the skill alongside it,
	// mirroring how the session runner copies SPEC.md into specs/<name>/.
	copyDir := filepath.Join(dir, "copy")
	if err := os.MkdirAll(copyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copySpec := filepath.Join(copyDir, "SPEC.md")
	if err := os.WriteFile(copySpec, []byte(specBody), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := CompileEnvironment(copySpec); err == nil {
		t.Fatal("expected unresolvable skill error without baseDir override")
	} else if !strings.Contains(err.Error(), "rel-skill") {
		t.Fatalf("unexpected error: %v", err)
	}

	compiled, err := CompileEnvironmentWithBase(copySpec, srcDir)
	if err != nil {
		t.Fatalf("CompileEnvironmentWithBase: %v", err)
	}
	found := false
	for _, s := range compiled.Skills {
		if s.Name == "rel-skill" {
			found = true
		}
	}
	if !found {
		t.Fatal("rel-skill should resolve via override baseDir")
	}
}

func TestCompileWithoutDeclaredSkillsHasNoSkills(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: cloud-default\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	if len(compiled.Skills) != 0 {
		t.Fatalf("undeclared skills were injected: %#v", compiled.Skills)
	}
}

func TestCompileIgnoresUnrelatedManifestJSON(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: app-manifest\nplatform: cloud\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"name":"app","icons":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	if len(compiled.Skills) != 0 {
		t.Fatalf("unrelated manifest.json injected skills: %#v", compiled.Skills)
	}
}

func TestCompileWithEmphasizedSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "critical-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: critical-skill\ndescription: Critical\n---\nMust do"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: emph-compile\nplatform: local\nskills:\n  - critical-skill*\n---\nBody"), 0o644)

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	// Should be in skills
	found := false
	for _, s := range compiled.Skills {
		if s.Name == "critical-skill" {
			found = true
		}
	}
	if !found {
		t.Error("critical-skill not in skills")
	}
	// Should be in required verifier skills
	found = false
	for _, s := range compiled.RequiredVerifierSkills {
		if s.Name == "critical-skill" {
			found = true
		}
	}
	if !found {
		t.Error("critical-skill not in required verifier skills")
	}
}

func TestToIRJSON(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: ir-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	ir := ToIRJSON(compiled)

	if ir["kind"] != "telos.compiled_environment.v1" {
		t.Errorf("kind: got %v", ir["kind"])
	}
	if ir["name"] != "ir-test" {
		t.Errorf("name: got %v", ir["name"])
	}
	if ir["platform"] != "local" {
		t.Errorf("platform: got %v", ir["platform"])
	}
	if _, ok := ir["extends"]; ok {
		t.Fatalf("extends should not be present in compiled IR: %#v", ir)
	}
	if _, ok := ir["lineage"]; ok {
		t.Fatalf("extends lineage should not be present in compiled IR: %#v", ir)
	}
}

func TestContentHashStability(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: stable-hash\nplatform: local\n---\nBody"), 0o644)

	c1, _ := CompileEnvironment(specPath)
	c2, _ := CompileEnvironment(specPath)

	if c1.ContentHash != c2.ContentHash {
		t.Errorf("content hash should be stable: %q vs %q", c1.ContentHash, c2.ContentHash)
	}
}

func TestRenderProverTask(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: render-test\nplatform: local\n---\n# Task\n\nDo something."), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "")

	if strings.Contains(task, "# Build:") || strings.Contains(task, "# Fix:") {
		t.Error("prover prompt should not derive build/fix semantics from the round number")
	}
	if !strings.Contains(task, "implementation agent") {
		t.Error("should contain implementation role prompt")
	}
	if !strings.Contains(task, "Do something.") {
		t.Error("should contain spec body")
	}
	if !strings.Contains(task, "# Spec") {
		t.Error("should contain spec section")
	}
	if strings.Contains(task, "## Requirements") {
		t.Error("should not use Requirements heading")
	}
	if !strings.Contains(task, "## Output") {
		t.Error("should contain output contract")
	}
}

func TestRenderVerifierTask(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: verify-test\nplatform: local\n---\n# Task\n\nCheck something."), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderVerifierTask(compiled, "")

	if strings.Contains(task, "# Verify:") {
		t.Error("verifier prompt should not use a synthetic title")
	}
	if !strings.Contains(task, "evaluation agent") {
		t.Error("should contain evaluation role prompt")
	}
	if !strings.Contains(task, "Check something.") {
		t.Error("should contain spec body")
	}
	if !strings.Contains(task, "including removal of superseded behavior") {
		t.Error("verifier prompt should check retirement as well as new behavior")
	}
}

func TestRenderVerifierTaskAllowsReusableEvaluationArtifacts(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: reusable-eval\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderVerifierTask(compiled, "")

	for _, want := range []string{
		"You may add and commit useful tests or probes",
		"do not change the implementation",
		"project's test location or `evaluation/`",
	} {
		if !strings.Contains(task, want) {
			t.Fatalf("verifier prompt missing %q:\n%s", want, task)
		}
	}
	if strings.Contains(task, "Keep evaluator scratch outside the delivered tree") {
		t.Fatalf("verifier prompt should not forbid durable evaluator artifacts:\n%s", task)
	}
}

func TestRenderProverRequiresCompleteOutcome(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: continuation-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "")

	if strings.Contains(task, "# Build:") || strings.Contains(task, "# Fix:") {
		t.Error("prover prompt should not use build/fix titles")
	}
	if !strings.Contains(task, "smallest complete solution") ||
		!strings.Contains(task, "Continue while you can make progress toward the goal") ||
		!strings.Contains(task, "Exercise your changes") {
		t.Error("prover prompt should require a complete outcome")
	}
	if strings.Contains(task, "smallest change that improves") ||
		strings.Contains(task, "Prefer incremental, inspectable changes") {
		t.Error("prover prompt should not encourage partial turns")
	}
}

func TestRenderWithSkillsRoster(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: A skill\n---\nInstructions"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: roster-test\nplatform: local\nskills:\n  - my-skill\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "")

	if !strings.Contains(task, "## Skills") {
		t.Error("should contain skills section")
	}
	if !strings.Contains(task, "`my-skill`") {
		t.Error("should contain skill name")
	}
	if strings.Contains(task, "Instructions") {
		t.Error("should reference skills without inlining their bodies")
	}
}

func TestRenderUsesDeclaredSkillsForBothRoles(t *testing.T) {
	dir := t.TempDir()
	defaultSkills := filepath.Join(dir, "default-skills")
	implSkillDir := filepath.Join(defaultSkills, "k8s-deploy")
	if err := os.MkdirAll(implSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(implSkillDir, "SKILL.md"), []byte("---\nname: k8s-deploy\ndescription: Deploy\n---\nDeploy."), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_SKILLS_DIR", defaultSkills)

	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: odoo\nplatform: cloud\nskills:\n  - k8s-deploy\n---\n# Odoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}

	proverTask := RenderProverTask(compiled, "")
	if !strings.Contains(proverTask, "`k8s-deploy`") {
		t.Fatalf("prover prompt missing declared skill:\n%s", proverTask)
	}

	verifierTask := RenderVerifierTask(compiled, "")
	if !strings.Contains(verifierTask, "`k8s-deploy`") {
		t.Fatalf("verifier prompt missing declared skill:\n%s", verifierTask)
	}
}

func TestRenderWithRequiredEvaluationSkills(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "crit-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: crit-skill\ndescription: Critical\n---\nMust follow"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: reqver-test\nplatform: local\nskills:\n  - crit-skill*\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)

	proverTask := RenderProverTask(compiled, "")
	verifierTask := RenderVerifierTask(compiled, "")
	for _, task := range []string{proverTask, verifierTask} {
		if !strings.Contains(task, "`crit-skill` - required evaluation rubric") {
			t.Error("required skill must be marked in both roles")
		}
		if strings.Contains(task, "Must follow") {
			t.Error("prompt should not inline skill instructions")
		}
	}
	for _, want := range []string{"Load every required rubric", "PASS or FAIL with evidence for each", "Any failure blocks concession"} {
		if !strings.Contains(verifierTask, want) {
			t.Errorf("verifier missing mandatory rubric instruction %q", want)
		}
	}
}

func TestRenderPersistentSessionContext(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: persistent-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "/tmp/transcript.md", PromptOptions{
		Persistent:      true,
		PrimarySpecPath: "/tmp/spec.md",
	})

	if !strings.Contains(task, "Lifecycle: `persistent`") {
		t.Error("prompt should identify the persistent lifecycle")
	}
	if strings.Contains(task, "`telos-orchestrate`") {
		t.Error("persistent session prompt should not auto-inject telos-orchestrate")
	}
	if !strings.Contains(task, "Primary spec: `/tmp/spec.md`") {
		t.Error("persistent session prompt should include primary spec path")
	}
}

func TestRenderTranscriptProtocolDoesNotDumpTranscript(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: transcript-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "/tmp/transcript.md")

	if !strings.Contains(task, "## Transcript") {
		t.Error("should contain transcript protocol section")
	}
	if !strings.Contains(task, "/tmp/transcript.md") {
		t.Error("should contain transcript path")
	}
	if strings.Contains(task, "Some history") || strings.Contains(task, "~~~~markdown") {
		t.Error("should not dump transcript content into task prompt")
	}
}

func TestRenderTranscriptProtocolRequiresReadFirst(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: transcript-read\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	proverTask := RenderProverTask(compiled, "/tmp/transcript.md")
	verifierTask := RenderVerifierTask(compiled, "/tmp/transcript.md")
	for _, task := range []string{proverTask, verifierTask} {
		for _, want := range []string{
			"Read the transcript for current spec updates and unresolved findings before acting",
			"read the current spec and available diff",
			"remove behavior that only served superseded requirements",
			"Preserve required data and history",
			"Reassess earlier findings and approvals against the current spec and state",
			"Do not edit the transcript",
		} {
			if !strings.Contains(task, want) {
				t.Errorf("prompt missing transcript guidance %q", want)
			}
		}
	}
}

func TestRenderOutputContractRequiresRegularProgressUpdates(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: progress-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	proverTask := RenderProverTask(compiled, "/tmp/transcript.md")
	verifierTask := RenderVerifierTask(compiled, "/tmp/transcript.md")

	for _, task := range []string{proverTask, verifierTask} {
		for _, want := range []string{
			"<progress_update>...</progress_update>",
			"meaningful changes, results, blockers, and long operations or waits",
			"report only observed progress",
			"final <progress_update>...</progress_update> in the same response",
		} {
			if !strings.Contains(task, want) {
				t.Fatalf("prompt missing progress guidance %q:\n%s", want, task)
			}
		}
		if strings.Contains(task, `phase=`) || strings.Contains(task, `timestamp=`) {
			t.Fatalf("progress guidance should not require model-owned metadata:\n%s", task)
		}
		for _, unwanted := range []string{"after planning", "after scoping", "about once per minute"} {
			if strings.Contains(task, unwanted) {
				t.Fatalf("progress guidance should not prescribe %q:\n%s", unwanted, task)
			}
		}
	}
	if !strings.Contains(verifierTask, "In one bounded pass, review every obligation") {
		t.Fatal("evaluation prompt should require a complete bounded review")
	}
}

func TestRenderVerifierTaskReviewBudgetUsesStatusContract(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: review-mode\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderVerifierTask(compiled, "/tmp/transcript.md", PromptOptions{
		ReviewBudget:   true,
		ReviewCycleCap: 2,
	})

	for _, want := range []string{
		"Review cycle cap: at most `2` verifier cycles",
		"exactly one status tag on its own final line",
		"<status>CONTINUE</status>",
		"<status>CONCEDE</status>",
	} {
		if !strings.Contains(task, want) {
			t.Fatalf("review-mode prompt missing %q:\n%s", want, task)
		}
	}
	for _, unwanted := range []string{
		"<review>",
		"<summary>",
		"criteria,score",
		"Do not emit <status> tags",
		"clear evaluation gradient",
		"next useful implementation pressure",
		"handoff summary for the next implementation turn",
	} {
		if strings.Contains(task, unwanted) {
			t.Fatalf("review-mode prompt should not contain stale pressure wording %q:\n%s", unwanted, task)
		}
	}
}

func TestRenderVerifierTaskAllowsWaitingOnlyForPersistentSessions(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: task-state\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderVerifierTask(compiled, "/tmp/transcript.md")
	if strings.Contains(task, "waiting for an inspected pending/running child") {
		t.Fatalf("bounded verifier must not concede while waiting for children:\n%s", task)
	}

	persistentTask := RenderVerifierTask(compiled, "/tmp/transcript.md", PromptOptions{Persistent: true})
	if !strings.Contains(persistentTask, "waiting for an inspected pending/running child") {
		t.Fatalf("persistent verifier should allow waiting for inspected children:\n%s", persistentTask)
	}
	if !strings.Contains(persistentTask, "Waiting does not mean the goal is complete") {
		t.Fatalf("waiting must not claim goal completion:\n%s", persistentTask)
	}
	if !strings.Contains(persistentTask, "Relevant failed or stopped children, and completed children with uninspected or missing expected results, are blockers") {
		t.Fatalf("persistent verifier should still block bad child state:\n%s", persistentTask)
	}
}

func TestRenderWorkspaceGuidance(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: ws-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "")

	if !strings.Contains(task, "## Workspace") {
		t.Error("should contain workspace section")
	}
	if !strings.Contains(task, "workspace.tar.gz") {
		t.Error("should describe child workspace checkpoints")
	}
	if strings.Contains(task, "/workspace/output") {
		t.Fatalf("workspace prompt should not hardcode container paths:\n%s", task)
	}
}
