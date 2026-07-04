package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	input := "---\nversion: v0\nname: test-spec\n---\n# Hello\nBody text"
	raw, body, ok := ParseFrontmatter(input)
	if !ok {
		t.Fatal("expected frontmatter to parse")
	}
	if raw["version"] != "v0" {
		t.Errorf("version: got %v", raw["version"])
	}
	if raw["name"] != "test-spec" {
		t.Errorf("name: got %v", raw["name"])
	}
	if !strings.Contains(body, "Body text") {
		t.Errorf("body: got %q", body)
	}
}

func TestParseFrontmatterNoFrontmatter(t *testing.T) {
	_, _, ok := ParseFrontmatter("# Just markdown\nNo frontmatter")
	if ok {
		t.Fatal("should not parse without frontmatter")
	}
}

func TestLoadEnvironment(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: my-test\nplatform: local\n---\n# My Test\n\nSpec body here."), 0o644)

	env, err := LoadEnvironment(specPath)
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if env.Name != "my-test" {
		t.Errorf("name: got %q", env.Name)
	}
	if env.Version != "v0" {
		t.Errorf("version: got %q", env.Version)
	}
	if env.PackageVersion != "" {
		t.Errorf("package version: got %q", env.PackageVersion)
	}
	if env.Platform != "local" {
		t.Errorf("platform: got %q", env.Platform)
	}
	if env.SpecText != "# My Test\n\nSpec body here." {
		t.Errorf("spec_text: got %q", env.SpecText)
	}
}

func TestLoadEnvironmentEmptyBody(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: empty-body\n---\n"), 0o644)

	_, err := LoadEnvironment(specPath)
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if !strings.Contains(err.Error(), "spec body is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadEnvironmentWithPackageVersion(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: 1.2.3\nname: versioned\n---\nBody"), 0o644)

	env, err := LoadEnvironment(specPath)
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if env.Version != "v0" {
		t.Errorf("schema version: got %q", env.Version)
	}
	if env.PackageVersion != "1.2.3" {
		t.Errorf("package version: got %q", env.PackageVersion)
	}
}

func TestLoadEnvironmentWithExplicitSchemaCompatibility(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nschema: v0\nversion: 1.2.3\nname: versioned\n---\nBody"), 0o644)

	env, err := LoadEnvironment(specPath)
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if env.Version != "v0" {
		t.Errorf("schema version: got %q", env.Version)
	}
	if env.PackageVersion != "1.2.3" {
		t.Errorf("package version: got %q", env.PackageVersion)
	}
}

func TestLoadEnvironmentInvalidSchema(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nschema: v99\nversion: 1.0.0\nname: bad-ver\n---\nBody"), 0o644)

	_, err := LoadEnvironment(specPath)
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
}

func TestLoadEnvironmentInvalidPlatform(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: bad-plat\nplatform: docker\n---\nBody"), 0o644)

	_, err := LoadEnvironment(specPath)
	if err == nil {
		t.Fatal("expected error for invalid platform")
	}
}

func TestLoadEnvironmentWithInterval(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: interval-test\ninterval: 15m\n---\nBody"), 0o644)

	env, err := LoadEnvironment(specPath)
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if env.IntervalSeconds == nil || *env.IntervalSeconds != 900 {
		t.Errorf("interval: got %v", env.IntervalSeconds)
	}
}

func TestLoadEnvironmentWithTags(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: tag-test\ntags:\n  - alpha\n  - beta\n---\nBody"), 0o644)

	env, err := LoadEnvironment(specPath)
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if len(env.Tags) != 2 || env.Tags[0] != "alpha" || env.Tags[1] != "beta" {
		t.Errorf("tags: got %v", env.Tags)
	}
}

func TestLoadEnvironmentWithSkills(t *testing.T) {
	dir := t.TempDir()
	// Create a skill
	skillDir := filepath.Join(dir, "my-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: A test skill\n---\nInstructions here"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: skill-test\nskills:\n  - my-skill\n---\nBody"), 0o644)

	env, err := LoadEnvironment(specPath)
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if len(env.SkillPaths) != 1 {
		t.Errorf("skill_paths: got %v", env.SkillPaths)
	}
}

func TestLoadEnvironmentWithEmphasizedSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "important-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: important-skill\ndescription: Critical\n---\nRequired instructions"), 0o644)

	specPath := filepath.Join(dir, "SPEC.md")
	os.WriteFile(specPath, []byte("---\nversion: v0\nname: emph-test\nskills:\n  - important-skill*\n---\nBody"), 0o644)

	env, err := LoadEnvironment(specPath)
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if len(env.SkillPaths) != 1 {
		t.Fatalf("expected 1 skill path, got %d", len(env.SkillPaths))
	}
	if len(env.RequiredVerifierSkillPaths) != 1 {
		t.Fatalf("expected 1 required verifier skill path, got %d", len(env.RequiredVerifierSkillPaths))
	}
	if env.SkillPaths[0] != env.RequiredVerifierSkillPaths[0] {
		t.Errorf("skill path mismatch: %s != %s", env.SkillPaths[0], env.RequiredVerifierSkillPaths[0])
	}
}

func TestSha256str(t *testing.T) {
	hash1 := sha256str("hello", "world")
	hash2 := sha256str("hello", "world")
	hash3 := sha256str("different")
	if hash1 != hash2 {
		t.Errorf("same input should produce same hash")
	}
	if hash1 == hash3 {
		t.Errorf("different input should produce different hash")
	}
	if len(hash1) != 16 {
		t.Errorf("expected 16 char hash, got %d", len(hash1))
	}
}
