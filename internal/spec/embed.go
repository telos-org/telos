package spec

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:prompts
var promptsFS embed.FS

// ReadPrompt reads an embedded prompt template by relative path.
func ReadPrompt(name string) (string, error) {
	data, err := promptsFS.ReadFile("prompts/" + name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// DefaultSkillsDir returns the external skill catalogue directory.
func DefaultSkillsDir() string {
	for _, dir := range []string{
		strings.TrimSpace(os.Getenv("TELOS_SKILLS_DIR")),
		assetsSkillsDir(),
		discoverSiblingAssetsSkillsDir(),
	} {
		if hasSkillCatalogue(dir) {
			return dir
		}
	}
	return ""
}

func assetsSkillsDir() string {
	dir := strings.TrimSpace(os.Getenv("TELOS_ASSETS_DIR"))
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "skills")
}

func discoverSiblingAssetsSkillsDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(wd, "assets", "skills")
		if hasSkillCatalogue(candidate) {
			return candidate
		}
		next := filepath.Dir(wd)
		if next == wd {
			return ""
		}
		wd = next
	}
}

func hasSkillCatalogue(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
