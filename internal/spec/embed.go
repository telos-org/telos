package spec

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed all:prompts
var promptsFS embed.FS

//go:embed all:skills
var skillsFS embed.FS

var (
	extractedSkillsDir string
	extractOnce        sync.Once
)

// ReadPrompt reads an embedded prompt template by relative path.
func ReadPrompt(name string) (string, error) {
	data, err := promptsFS.ReadFile("prompts/" + name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// BuiltinSkillsDir returns the path to extracted built-in skills.
// Skills are extracted to a temp dir on first call.
func BuiltinSkillsDir() string {
	extractOnce.Do(func() {
		dir, err := extractEmbeddedSkills()
		if err != nil {
			return
		}
		extractedSkillsDir = dir
	})
	return extractedSkillsDir
}

func extractEmbeddedSkills() (string, error) {
	tmpDir, err := os.MkdirTemp("", "telos-skills-*")
	if err != nil {
		return "", err
	}
	return tmpDir, extractDir(skillsFS, "skills", tmpDir)
}

func extractDir(fs embed.FS, prefix, dest string) error {
	entries, err := fs.ReadDir(prefix)
	if err != nil {
		return err
	}
	for _, e := range entries {
		src := prefix + "/" + e.Name()
		dst := filepath.Join(dest, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			if err := extractDir(fs, src, dst); err != nil {
				return err
			}
		} else {
			data, err := fs.ReadFile(src)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
