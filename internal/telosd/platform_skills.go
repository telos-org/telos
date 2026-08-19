package telosd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/spec"
)

const platformSkillsEnv = "TELOS_PLATFORM_SKILLS"

var (
	platformSkillNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	platformSkillDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type platformSkill struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

func installPlatformSkills(ctx context.Context, materializer *applyPackageMaterializer) error {
	skills, err := configuredPlatformSkills()
	if err != nil || len(skills) == 0 {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve agent home: %w", err)
	}
	root := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create platform skills root: %w", err)
	}
	for _, skill := range skills {
		bundle, err := materializer.ensureSkillBundle(ctx, skill.Name, skill.Digest)
		if err != nil {
			return fmt.Errorf("fetch platform skill %q: %w", skill.Name, err)
		}
		if err := installPlatformSkill(root, skill, bundle); err != nil {
			return err
		}
	}
	return nil
}

func configuredPlatformSkills() ([]platformSkill, error) {
	raw := strings.TrimSpace(os.Getenv(platformSkillsEnv))
	if raw == "" {
		return nil, nil
	}
	var skills []platformSkill
	if err := json.Unmarshal([]byte(raw), &skills); err != nil {
		return nil, fmt.Errorf("parse %s: %w", platformSkillsEnv, err)
	}
	seen := map[string]bool{}
	for index := range skills {
		skills[index].Name = strings.TrimSpace(skills[index].Name)
		skills[index].Digest = strings.TrimSpace(skills[index].Digest)
		skill := skills[index]
		if !platformSkillNamePattern.MatchString(skill.Name) {
			return nil, fmt.Errorf("invalid platform skill name %q", skill.Name)
		}
		if !platformSkillDigestPattern.MatchString(skill.Digest) {
			return nil, fmt.Errorf("invalid platform skill digest for %q", skill.Name)
		}
		if seen[skill.Name] {
			return nil, fmt.Errorf("duplicate platform skill %q", skill.Name)
		}
		seen[skill.Name] = true
	}
	return skills, nil
}

func installPlatformSkill(root string, skill platformSkill, bundle []byte) error {
	stage, err := os.MkdirTemp(root, "."+skill.Name+"-")
	if err != nil {
		return fmt.Errorf("stage platform skill %q: %w", skill.Name, err)
	}
	defer os.RemoveAll(stage)
	if err := spec.ExtractSkillBundle(skill.Name, skill.Digest, bundle, stage); err != nil {
		return fmt.Errorf("extract platform skill %q: %w", skill.Name, err)
	}
	if _, err := os.Stat(filepath.Join(stage, "SKILL.md")); err != nil {
		return fmt.Errorf("platform skill %q missing SKILL.md: %w", skill.Name, err)
	}
	target := filepath.Join(root, skill.Name)
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("replace platform skill %q: %w", skill.Name, err)
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("install platform skill %q: %w", skill.Name, err)
	}
	return nil
}
