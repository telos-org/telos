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
	task := RenderTask(RoleProver, compiled, "")

	if strings.Contains(task, "# Build:") || strings.Contains(task, "# Fix:") {
		t.Error("prover prompt should not derive build/fix semantics from the round number")
	}
	rolePrompt, err := ReadPrompt("prover.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(task, rolePrompt) {
		t.Error("should begin with the embedded prover role prompt")
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
}

func TestRenderVerifierTask(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: verify-test\nplatform: local\n---\n# Task\n\nCheck something."), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderTask(RoleVerifier, compiled, "")

	if strings.Contains(task, "# Verify:") {
		t.Error("verifier prompt should not use a synthetic title")
	}
	rolePrompt, err := ReadPrompt("verifier.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(task, rolePrompt) {
		t.Error("should begin with the embedded verifier role prompt")
	}
	if !strings.Contains(task, "Check something.") {
		t.Error("should contain spec body")
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
	task := RenderTask(RoleProver, compiled, "")

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

	proverTask := RenderTask(RoleProver, compiled, "")
	if !strings.Contains(proverTask, "`k8s-deploy`") {
		t.Fatalf("prover prompt missing declared skill:\n%s", proverTask)
	}

	verifierTask := RenderTask(RoleVerifier, compiled, "")
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

	proverTask := RenderTask(RoleProver, compiled, "")
	verifierTask := RenderTask(RoleVerifier, compiled, "")
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

func TestRenderIncludesSpecSource(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: persistent-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	for _, role := range []Role{RoleProver, RoleVerifier} {
		task := RenderTask(role, compiled, "/tmp/transcript.md")
		for _, want := range []string{
			"name: `" + compiled.Environment.Name + "`",
			"version: " + compiled.Environment.Version,
			"Source: `" + specPath + "`",
		} {
			if !strings.Contains(task, want) {
				t.Errorf("prompt missing compiled spec identity: %q", want)
			}
		}
	}
}

func TestRenderReferencesTranscriptWithoutInliningHistory(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: transcript-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	transcriptPath := filepath.Join(dir, "transcript.md")
	if err := os.WriteFile(transcriptPath, []byte("Some history"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{RoleProver, RoleVerifier} {
		task := RenderTask(role, compiled, transcriptPath)
		if !strings.Contains(task, transcriptPath) {
			t.Error("should contain transcript path")
		}
		if strings.Contains(task, "Some history") || strings.Contains(task, "~~~~markdown") {
			t.Error("should not dump transcript content into task prompt")
		}
	}
}

func TestRenderIncludesProgressProtocol(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: progress-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	proverTask := RenderTask(RoleProver, compiled, "/tmp/transcript.md")
	verifierTask := RenderTask(RoleVerifier, compiled, "/tmp/transcript.md")

	for _, task := range []string{proverTask, verifierTask} {
		for _, want := range []string{
			"<progress_update>",
			"</progress_update>",
		} {
			if !strings.Contains(task, want) {
				t.Fatalf("prompt missing progress protocol %q:\n%s", want, task)
			}
		}
	}
}

func TestRenderVerifierUsesStatusContract(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: review-mode\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	task := RenderTask(RoleVerifier, compiled, "/tmp/transcript.md")

	for _, want := range []string{
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
		"<user_update>",
	} {
		if strings.Contains(task, unwanted) {
			t.Fatalf("review-mode prompt should not contain stale pressure wording %q:\n%s", unwanted, task)
		}
	}
}

func TestOnlyTheRootVerifierIsOfferedBlocked(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: blocked\nplatform: local\n---\nBody"), 0o644)
	compiled, _ := CompileEnvironment(specPath)

	root := RenderRootVerifierTask(compiled, "/tmp/transcript.md")
	if !strings.Contains(root, "<status>BLOCKED</status> only if the goal spec/skills cannot be satisfied as written in this environment") ||
		!strings.Contains(root, "<blocked>...</blocked>") {
		t.Fatalf("root verifier should be able to block an unsatisfiable spec:\n%s", root)
	}
	for _, role := range []Role{RoleVerifier, RoleProver} {
		if task := RenderTask(role, compiled, "/tmp/transcript.md"); strings.Contains(task, "BLOCKED") {
			t.Fatalf("%s must not be offered BLOCKED:\n%s", role, task)
		}
	}
}

func TestRenderDoesNotInjectSessionMetadata(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: ws-test\nplatform: local\n---\nBody"), 0o644)

	compiled, _ := CompileEnvironment(specPath)
	for _, task := range []string{RenderTask(RoleProver, compiled, ""), RenderTask(RoleVerifier, compiled, "")} {
		for _, unwanted := range []string{
			"## Session", "Namespace:", "Runtime:", "Lifecycle:", "Review cycle cap:",
			"workspace.tar.gz", "CLI credentials", "telos apply", "child", compiled.Namespace,
		} {
			if strings.Contains(task, unwanted) {
				t.Errorf("prompt should not inject session metadata or child instructions: %q", unwanted)
			}
		}
	}
}
