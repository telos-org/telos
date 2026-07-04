package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileEnvironment(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: compile-test\nplatform: local\n---\n# Compile Test\n\nTest body."), 0o644)

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
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: skill-compile\nplatform: local\nskills:\n  - my-skill\n---\nBody"), 0o644)

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

func TestCompileWithExtendsUsesParentNamespaceAndHash(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(basePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(basePath, []byte("---\nversion: v0\nname: base-spec\nplatform: local\n---\nBase body"), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(dir, "child", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(childPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte("---\nversion: v0\nname: child-spec\nplatform: local\nextends: ../base/SPEC.md\n---\nChild body"), 0o644); err != nil {
		t.Fatal(err)
	}

	base, err := CompileEnvironment(basePath)
	if err != nil {
		t.Fatalf("CompileEnvironment base: %v", err)
	}
	child, err := CompileEnvironment(childPath)
	if err != nil {
		t.Fatalf("CompileEnvironment child: %v", err)
	}
	if child.ExtendsCompiled == nil {
		t.Fatal("expected child to keep compiled parent")
	}
	if child.Namespace != base.Namespace {
		t.Fatalf("namespace: got %q, want %q", child.Namespace, base.Namespace)
	}
	if len(child.Lineage) != 1 || child.Lineage[0] != base.Namespace {
		t.Fatalf("lineage: got %#v", child.Lineage)
	}
	ir := ToIRJSON(child)
	extends, ok := ir["extends"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected extends in IR, got %#v", ir["extends"])
	}
	if extends["name"] != "base-spec" || extends["namespace"] != base.Namespace {
		t.Fatalf("unexpected extends IR: %#v", extends)
	}

	originalHash := child.ContentHash
	if err := os.WriteFile(basePath, []byte("---\nversion: v0\nname: base-spec\nplatform: local\n---\nChanged base body"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := CompileEnvironment(childPath)
	if err != nil {
		t.Fatalf("CompileEnvironment changed child: %v", err)
	}
	if changed.ContentHash == originalHash {
		t.Fatal("child hash should change when extended parent changes")
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
	specBody := "---\nversion: v0\nname: rel-base-test\nplatform: local\nskills:\n  - skills/rel-skill\n---\nBody"
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

func TestCompileWithAbsoluteExtendsPath(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(basePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(basePath, []byte("---\nversion: v0\nname: base-abs\nplatform: local\n---\nBase body"), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(dir, "child", "SPEC.md")
	if err := os.MkdirAll(filepath.Dir(childPath), 0o755); err != nil {
		t.Fatal(err)
	}
	childSpec := fmt.Sprintf("---\nversion: v0\nname: child-abs\nplatform: local\nextends: %s\n---\nChild body", basePath)
	if err := os.WriteFile(childPath, []byte(childSpec), 0o644); err != nil {
		t.Fatal(err)
	}

	child, err := CompileEnvironment(childPath)
	if err != nil {
		t.Fatalf("CompileEnvironment child: %v", err)
	}
	if child.ExtendsCompiled == nil {
		t.Fatal("expected child to keep compiled parent")
	}
	if child.Environment.ExtendsPath != basePath {
		t.Fatalf("extends path: got %q, want %q", child.Environment.ExtendsPath, basePath)
	}
}

func TestCompileWithoutDeclaredSkillsOnlyIncludesVerifierSkills(t *testing.T) {
	dir := t.TempDir()
	defaultSkills := filepath.Join(dir, "default-skills")
	for _, name := range []string{"verify-engineering", "verify-quality"} {
		skillDir := filepath.Join(defaultSkills, name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+name+"\n---\nVerify."), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TELOS_SKILLS_DIR", defaultSkills)

	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: v0\nname: cloud-default\n---\nBody"), 0o644); err != nil {
		t.Fatal(err)
	}

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	names := map[string]bool{}
	for _, s := range compiled.Skills {
		names[s.Name] = true
	}
	if names["catalogue-deploy"] {
		t.Fatal("skills must be explicit; cloud specs should not implicitly load catalogue skills")
	}
	if !names["verify-engineering"] || !names["verify-quality"] {
		t.Fatal("expected default verifier skills")
	}
}

func TestCompileWithEmphasizedSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "critical-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: critical-skill\ndescription: Critical\n---\nMust do"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: emph-compile\nplatform: local\nskills:\n  - critical-skill*\n---\nBody"), 0o644)

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
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: ir-test\nplatform: local\n---\nBody"), 0o644)

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
}

func TestContentHashStability(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: stable-hash\nplatform: local\n---\nBody"), 0o644)

	c1, _ := CompileEnvironment(specPath)
	c2, _ := CompileEnvironment(specPath)

	if c1.ContentHash != c2.ContentHash {
		t.Errorf("content hash should be stable: %q vs %q", c1.ContentHash, c2.ContentHash)
	}
}

func TestRenderProverTask(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: render-test\nplatform: local\n---\n# Task\n\nDo something."), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "", "")

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
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: verify-test\nplatform: local\n---\n# Task\n\nCheck something."), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderVerifierTask(compiled, "", "")

	if strings.Contains(task, "# Verify:") {
		t.Error("verifier prompt should not use a synthetic title")
	}
	if !strings.Contains(task, "evaluation agent") {
		t.Error("should contain evaluation role prompt")
	}
	if !strings.Contains(task, "Check something.") {
		t.Error("should contain spec body")
	}
}

func TestRenderVerifierTaskAllowsReusableEvaluationArtifacts(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: reusable-eval\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderVerifierTask(compiled, "=== FILES ===\n./main.go", "")

	for _, want := range []string{
		"Persist Useful Probes",
		"write to the same workspace when the change is evaluation code",
		"integration probes, fixtures, scripts, or minimal counterexamples",
		"natural test location or a small `evaluation/` directory",
	} {
		if !strings.Contains(task, want) {
			t.Fatalf("verifier prompt missing %q:\n%s", want, task)
		}
	}
	if strings.Contains(task, "Keep evaluator scratch outside the delivered tree") {
		t.Fatalf("verifier prompt should not forbid durable evaluator artifacts:\n%s", task)
	}
}

func TestRenderProverUsesOperatingPosture(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: continuation-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "", "")

	if strings.Contains(task, "# Build:") || strings.Contains(task, "# Fix:") {
		t.Error("prover prompt should not use build/fix titles")
	}
	if !strings.Contains(task, "continue from the append-only transcript") {
		t.Error("prover prompt should describe continuation through transcript/workspace")
	}
}

func TestRenderWithSkillsRoster(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: A skill\n---\nInstructions"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: roster-test\nplatform: local\nskills:\n  - my-skill\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "", "")

	if !strings.Contains(task, "## Skills") {
		t.Error("should contain skills section")
	}
	if !strings.Contains(task, "`my-skill`") {
		t.Error("should contain skill name")
	}
	if !strings.Contains(task, "prompts reference names instead of inlining skill bodies") {
		t.Error("should explain skill-name routing without inlining skill bodies")
	}
}

func TestRenderWithRequiredEvaluationSkills(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "crit-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: crit-skill\ndescription: Critical\n---\nMust follow"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: reqver-test\nplatform: local\nskills:\n  - crit-skill*\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)

	proverTask := RenderProverTask(compiled, "", "")
	if !strings.Contains(proverTask, "Required Evaluation Rubrics") {
		t.Error("prover should see required evaluation rubrics")
	}
	if !strings.Contains(proverTask, "load these starred skills by name") {
		t.Error("prover should see skill-name rubric guidance")
	}
	if !strings.Contains(proverTask, "required evaluation rubric") {
		t.Error("prover should see required marker in skills roster")
	}
	if strings.Contains(proverTask, "Must follow") {
		t.Error("prover prompt should not inline skill instructions")
	}

	verifierTask := RenderVerifierTask(compiled, "", "")
	if !strings.Contains(verifierTask, "Required Evaluation Rubrics") {
		t.Error("verifier should see required evaluation rubrics")
	}
	if !strings.Contains(verifierTask, "mandatory grading rubrics") {
		t.Error("verifier should see rubric instructions")
	}
	if !strings.Contains(verifierTask, "Use each mounted skill by name") {
		t.Error("verifier should see mounted skill-name guidance")
	}
	if !strings.Contains(verifierTask, "`crit-skill`") {
		t.Error("verifier should see required skill name")
	}
	if strings.Contains(verifierTask, "Must follow") {
		t.Error("verifier prompt should not inline skill instructions")
	}
}

func TestRenderControllerPromptIncludesOrchestrationSkill(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: controller-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "", "/tmp/transcript.md", PromptOptions{
		Controller:      true,
		PrimarySpecPath: "/tmp/spec.md",
	})

	if !strings.Contains(task, "## Controller Role") {
		t.Error("controller prompt should include controller role guidance")
	}
	if !strings.Contains(task, "`telos-orchestrate`") {
		t.Error("controller prompt should include telos-orchestrate in effective skills")
	}
	if !strings.Contains(task, "Primary spec: `/tmp/spec.md`") {
		t.Error("controller prompt should include primary spec path")
	}
}

func TestRenderTranscriptProtocolDoesNotDumpTranscript(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: transcript-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "", "/tmp/transcript.md")

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
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: transcript-read\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	proverTask := RenderProverTask(compiled, "", "/tmp/transcript.md")

	if !strings.Contains(proverTask, "First action every turn: read this transcript path") {
		t.Error("implementation prompt should require reading transcript first")
	}
	if !strings.Contains(proverTask, "If the transcript only contains the header, proceed from scratch against the spec") {
		t.Error("implementation prompt should explain first-turn/header-only transcript")
	}
	if !strings.Contains(proverTask, "identify unresolved evaluator findings") {
		t.Error("implementation prompt should require identifying unresolved evaluator findings")
	}

	verifierTask := RenderVerifierTask(compiled, "", "/tmp/transcript.md")
	if !strings.Contains(verifierTask, "First action every turn: read this transcript path") {
		t.Error("evaluation prompt should require reading transcript first")
	}
	if !strings.Contains(verifierTask, "identify the implementation claims") {
		t.Error("evaluation prompt should require identifying implementation claims")
	}
}

func TestRenderOutputContractRequiresRegularProgressUpdates(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: progress-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	proverTask := RenderProverTask(compiled, "", "/tmp/transcript.md")
	verifierTask := RenderVerifierTask(compiled, "", "/tmp/transcript.md")

	for _, task := range []string{proverTask, verifierTask} {
		for _, want := range []string{
			"meaningful milestones",
			"Before any operation expected to take more than 60 seconds",
			"about once per minute",
			"do not save all progress for the final response",
		} {
			if !strings.Contains(task, want) {
				t.Fatalf("prompt missing progress guidance %q:\n%s", want, task)
			}
		}
		if strings.Contains(task, `phase=`) || strings.Contains(task, `timestamp=`) {
			t.Fatalf("progress guidance should not require model-owned metadata:\n%s", task)
		}
	}
}

func TestRenderVerifierTaskReviewModeUsesReviewContract(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: review-mode\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderVerifierTask(compiled, "", "/tmp/transcript.md", PromptOptions{
		ReviewMode:   true,
		ReviewCycles: 2,
	})

	for _, want := range []string{
		"<review>",
		"<summary>",
		"criteria,score",
		"Review cycles requested: `2`",
		"evaluation decision, not a termination decision",
		"no implementation change is recommended",
		"preserve the current shape",
		"revalidate tests, tree state, and named invariants",
		"distinguish grounded fixes from speculative residual uncertainty",
	} {
		if !strings.Contains(task, want) {
			t.Fatalf("review-mode prompt missing %q:\n%s", want, task)
		}
	}
	for _, unwanted := range []string{
		"clear evaluation gradient",
		"next useful implementation pressure",
		"handoff summary for the next implementation turn",
	} {
		if strings.Contains(task, unwanted) {
			t.Fatalf("review-mode prompt should not contain stale pressure wording %q:\n%s", unwanted, task)
		}
	}
	if strings.Contains(task, "CONCEDE") || strings.Contains(task, "CONTINUE") {
		t.Fatalf("review-mode prompt should not contain status control tokens:\n%s", task)
	}
}

func TestRenderVerifierTaskGatesControllerOnlyTaskState(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: task-state\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderVerifierTask(compiled, "", "/tmp/transcript.md")
	if strings.Contains(task, "if any required task is pending") {
		t.Fatalf("leaf task verifier should not include controller task-state rule:\n%s", task)
	}

	controllerTask := RenderVerifierTask(compiled, "", "/tmp/transcript.md", PromptOptions{Controller: true})
	if !strings.Contains(controllerTask, "pending or running child task is valid waiting work") {
		t.Fatalf("controller verifier should include controller task-state rule:\n%s", controllerTask)
	}
	if !strings.Contains(controllerTask, "CONCEDE</status> for that cycle if the correct next controller action is simply to wait") {
		t.Fatalf("controller verifier should allow clean wait cycles:\n%s", controllerTask)
	}
	if !strings.Contains(controllerTask, "CONTINUE</status> if a child is stopped, failed, terminal but uninspected") {
		t.Fatalf("controller verifier should still block bad child state:\n%s", controllerTask)
	}
}

func TestRenderWithWorkspace(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: ws-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderProverTask(compiled, "=== FILES ===\n./main.go", "")

	if !strings.Contains(task, "## Workspace") {
		t.Error("should contain workspace section")
	}
	if !strings.Contains(task, "./main.go") {
		t.Error("should contain workspace content")
	}
	if strings.Contains(task, "/workspace/output") {
		t.Fatalf("workspace prompt should not hardcode container paths:\n%s", task)
	}
}
