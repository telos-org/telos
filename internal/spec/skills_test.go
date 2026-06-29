package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkill(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "test-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(
		"---\nname: test-skill\ndescription: A test skill for testing\ncategory: testing\nrole: ignored\n---\n# Instructions\n\nDo stuff."), 0o644)

	s, err := LoadSkill(skillDir)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	if s.Name != "test-skill" {
		t.Errorf("name: got %q", s.Name)
	}
	if s.Description != "A test skill for testing" {
		t.Errorf("description: got %q", s.Description)
	}
	if !strings.Contains(s.Instructions, "Do stuff.") {
		t.Errorf("instructions: got %q", s.Instructions)
	}
	if len(s.Tags) != 1 || s.Tags[0] != "testing" {
		t.Errorf("tags should include category only, got %#v", s.Tags)
	}
}

func TestLoadSkillNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "plain-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Plain Skill\n\nJust instructions."), 0o644)

	s, err := LoadSkill(skillDir)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	if s.Name != "plain-skill" {
		t.Errorf("name should be dir name: got %q", s.Name)
	}
	if !strings.Contains(s.Instructions, "Just instructions.") {
		t.Errorf("instructions: got %q", s.Instructions)
	}
}

func TestLoadSkillWithScripts(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "scripted-skill")
	os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: scripted\n---\nInstructions"), 0o644)
	os.WriteFile(filepath.Join(skillDir, "scripts", "run.py"), []byte("print('hello')"), 0o644)
	os.WriteFile(filepath.Join(skillDir, "scripts", "setup.sh"), []byte("echo setup"), 0o644)

	s, err := LoadSkill(skillDir)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	if len(s.Scripts) != 2 {
		t.Fatalf("expected 2 scripts, got %d", len(s.Scripts))
	}
	// Should be sorted
	if s.Scripts[0].Name != "run.py" {
		t.Errorf("first script: got %q", s.Scripts[0].Name)
	}
	if s.Scripts[0].Language != "python" {
		t.Errorf("first language: got %q", s.Scripts[0].Language)
	}
	if s.Scripts[1].Name != "setup.sh" {
		t.Errorf("second script: got %q", s.Scripts[1].Name)
	}
	if s.Scripts[1].Language != "bash" {
		t.Errorf("second language: got %q", s.Scripts[1].Language)
	}
}

func TestResolveSkillsFromPaths(t *testing.T) {
	dir := t.TempDir()

	// Single skill
	skillDir := filepath.Join(dir, "skill-a")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: skill-a\n---\nA"), 0o644)

	// Skill dir with children
	multiDir := filepath.Join(dir, "skills-dir")
	for _, name := range []string{"child-x", "child-y"} {
		child := filepath.Join(multiDir, name)
		os.MkdirAll(child, 0o755)
		os.WriteFile(filepath.Join(child, "SKILL.md"), []byte("---\nname: "+name+"\n---\nInstructions"), 0o644)
	}

	skills, err := ResolveSkillsFromPaths([]string{skillDir, multiDir})
	if err != nil {
		t.Fatalf("ResolveSkillsFromPaths: %v", err)
	}
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	for _, want := range []string{"skill-a", "child-x", "child-y"} {
		if !names[want] {
			t.Errorf("missing skill %q", want)
		}
	}
}

func TestResolveSkillsDedup(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "dup-skill")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: dup-skill\n---\nInstructions"), 0o644)

	// Pass same path twice
	skills, err := ResolveSkillsFromPaths([]string{skillDir, skillDir})
	if err != nil {
		t.Fatalf("ResolveSkillsFromPaths: %v", err)
	}
	if len(skills) != 1 {
		t.Errorf("expected dedup to 1, got %d", len(skills))
	}
}

func TestDefaultBuildDashboardIncludesReferences(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeTestSkill(t, dir, "build-dashboard", "---\nname: build-dashboard\n---\nUse dashboard.")
	referenceDir := filepath.Join(skillDir, "reference")
	if err := os.MkdirAll(referenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"components.jsx", "theme.css", "theme.js"} {
		if err := os.WriteFile(filepath.Join(referenceDir, name), []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TELOS_SKILLS_DIR", dir)

	s := ResolveDefaultSkill("build-dashboard")
	if s == nil {
		t.Fatal("expected default build-dashboard skill")
	}
	if strings.Contains(s.Instructions, "tokens.js") {
		t.Fatal("build-dashboard should not reference removed tokens.js")
	}
	for _, name := range []string{"components.jsx", "theme.css", "theme.js"} {
		data, err := os.ReadFile(filepath.Join(s.Path, "reference", name))
		if err != nil {
			t.Fatalf("expected build-dashboard reference %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("expected build-dashboard reference %s to be non-empty", name)
		}
	}
}

func writeTestSkill(t *testing.T, root, name, data string) string {
	t.Helper()
	skillDir := filepath.Join(root, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return skillDir
}
