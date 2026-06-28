package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultVerifierSkills are the names of always-included verifier skills.
var DefaultVerifierSkills = []string{
	"verify-quality",
	"verify-engineering",
}

var scriptExtensions = map[string]string{
	".py":   "python",
	".sh":   "bash",
	".bash": "bash",
}

// SkillScript is a script file bundled with a skill.
type SkillScript struct {
	Name     string
	Language string
	Content  string
}

// Skill is a resolved skill with metadata, instructions, and bundled scripts.
type Skill struct {
	Name         string
	Description  string
	Instructions string
	Path         string
	Tags         []string
	Scripts      []*SkillScript
}

var skillFrontmatterRE = regexp.MustCompile(`(?s)\A---\s*\n(.*?\n)---\s*\n?(.*)`)

// LoadSkill loads one skill directory from disk.
func LoadSkill(skillDir string) (*Skill, error) {
	skillMD := filepath.Join(skillDir, "SKILL.md")
	data, err := os.ReadFile(skillMD)
	if err != nil {
		return nil, fmt.Errorf("skill %s: %w", skillDir, err)
	}
	content := string(data)
	name := filepath.Base(skillDir)
	description := ""
	instructions := content
	var tags []string

	m := skillFrontmatterRE.FindStringSubmatch(content)
	if m != nil {
		var raw map[string]interface{}
		if err := yaml.Unmarshal([]byte(m[1]), &raw); err == nil && raw != nil {
			instructions = strings.TrimSpace(m[2])
			if n, ok := raw["name"].(string); ok && n != "" {
				name = strings.TrimSpace(n)
			}
			if d, ok := raw["description"].(string); ok {
				description = strings.TrimSpace(d)
			}
			if cat, ok := raw["category"].(string); ok && cat != "" {
				tags = append(tags, strings.TrimSpace(cat))
			}
		}
	}

	scripts := loadScripts(skillDir)
	return &Skill{
		Name:         name,
		Description:  description,
		Instructions: instructions,
		Path:         skillDir,
		Tags:         tags,
		Scripts:      scripts,
	}, nil
}

func loadScripts(skillDir string) []*SkillScript {
	scriptsDir := filepath.Join(skillDir, "scripts")
	entries, err := os.ReadDir(scriptsDir)
	if err != nil {
		return nil
	}
	var scripts []*SkillScript
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		lang, ok := scriptExtensions[ext]
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(scriptsDir, e.Name()))
		if err != nil {
			continue
		}
		scripts = append(scripts, &SkillScript{
			Name:     e.Name(),
			Language: lang,
			Content:  string(data),
		})
	}
	sort.Slice(scripts, func(i, j int) bool { return scripts[i].Name < scripts[j].Name })
	return scripts
}

// ResolveSkillsFromPaths resolves skills from declared spec paths.
func ResolveSkillsFromPaths(paths []string) ([]*Skill, error) {
	seen := map[string]*Skill{}
	var order []string
	for _, p := range paths {
		abs, _ := filepath.Abs(p)
		if info, err := os.Stat(abs); err == nil {
			if !info.IsDir() && filepath.Base(abs) == "SKILL.md" {
				abs = filepath.Dir(abs)
			}
		}
		skills, err := resolveOnePath(abs)
		if err != nil {
			return nil, err
		}
		for _, s := range skills {
			key, _ := filepath.Abs(s.Path)
			if _, exists := seen[key]; !exists {
				seen[key] = s
				order = append(order, key)
			}
		}
	}
	var result []*Skill
	for _, k := range order {
		result = append(result, seen[k])
	}
	return result, nil
}

func resolveOnePath(abs string) ([]*Skill, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, nil
	}
	if !info.IsDir() {
		return nil, nil
	}
	// Single skill dir?
	if _, err := os.Stat(filepath.Join(abs, "SKILL.md")); err == nil {
		s, err := LoadSkill(abs)
		if err != nil {
			return nil, err
		}
		return []*Skill{s}, nil
	}
	// Directory of skill subdirs
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, nil
	}
	var skills []*Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(abs, e.Name())
		if _, err := os.Stat(filepath.Join(child, "SKILL.md")); err == nil {
			s, loadErr := LoadSkill(child)
			if loadErr != nil {
				continue
			}
			skills = append(skills, s)
		}
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, nil
}

// ResolveDefaultVerifierSkills loads the default verifier skills.
func ResolveDefaultVerifierSkills() []*Skill {
	dir := DefaultSkillsDir()
	if dir == "" {
		return nil
	}
	var skills []*Skill
	for _, name := range DefaultVerifierSkills {
		skillDir := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			continue
		}
		s, err := LoadSkill(skillDir)
		if err != nil {
			continue
		}
		skills = append(skills, s)
	}
	return skills
}

// ResolveDefaultSkill loads a single default catalogue skill by name.
func ResolveDefaultSkill(name string) *Skill {
	dir := DefaultSkillsDir()
	if dir == "" {
		return nil
	}
	skillDir := filepath.Join(dir, name)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return nil
	}
	s, err := LoadSkill(skillDir)
	if err != nil {
		return nil
	}
	return s
}
