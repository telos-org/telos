package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRegistrySkillRef(t *testing.T) {
	ref, ok := ParseRegistrySkillRef("skill:@telos/verify-quality:1.2.3*")
	if !ok {
		t.Fatal("expected valid registry ref")
	}
	if ref.Scope != "telos" || ref.Name != "verify-quality" || ref.Version != "1.2.3" || ref.Ref != "@telos/verify-quality:1.2.3" {
		t.Fatalf("ref: got %#v", ref)
	}
	for _, invalid := range []string{
		"@telos/verify-quality:",
		"@telos/verify-quality:latest",
		"@Telos/verify-quality:1.2.3",
	} {
		if _, ok := ParseRegistrySkillRef(invalid); ok {
			t.Fatalf("expected invalid registry ref: %s", invalid)
		}
	}
}

func TestRegistrySkillRefsIncludesExtendsChain(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent.md")
	child := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(parent, []byte("---\nversion: 0.1.0\nname: parent\nskills: '@acme/base:2.0.0'\n---\nParent."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("---\nversion: 0.1.0\nname: child\nextends: ./parent.md\nskills:\n  - '@telos/check:1.2.3*'\n  - '@acme/base:2.0.0'\n---\nChild."), 0o644); err != nil {
		t.Fatal(err)
	}

	refs, err := RegistrySkillRefs(child)
	if err != nil {
		t.Fatalf("RegistrySkillRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs: got %#v", refs)
	}
	if refs[0].Ref != "@acme/base:2.0.0" || refs[1].Ref != "@telos/check:1.2.3" {
		t.Fatalf("refs: got %#v", refs)
	}
}

func TestLoadEnvironmentPrefersExactRegistryCache(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	catalogue := filepath.Join(dir, "catalogue")
	writePackageTestSkill(t, catalogue, "deploy", map[string]string{
		"SKILL.md": "---\nname: deploy\n---\nCatalogue copy.",
	})
	t.Setenv("TELOS_SKILLS_DIR", catalogue)
	t.Setenv(RegistrySkillsDirEnv, cache)
	ref, ok := ParseRegistrySkillRef("@telos/deploy:1.0.0")
	if !ok {
		t.Fatal("expected registry ref")
	}
	cached := RegistrySkillPath(ref)
	writePackageTestSkill(t, filepath.Dir(cached), filepath.Base(cached), map[string]string{
		"SKILL.md": "---\nname: deploy\n---\nRegistry copy.",
	})
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: 0.1.0\nname: cached\nskills: '@telos/deploy:1.0.0'\n---\nBody."), 0o644); err != nil {
		t.Fatal(err)
	}

	env, err := LoadEnvironment(specPath)
	if err != nil {
		t.Fatalf("LoadEnvironment: %v", err)
	}
	if len(env.SkillPaths) != 1 || env.SkillPaths[0] != cached {
		t.Fatalf("skill paths: got %#v", env.SkillPaths)
	}
}
