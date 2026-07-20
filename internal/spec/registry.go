package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const RegistrySkillsDirEnv = "TELOS_REGISTRY_SKILLS_DIR"

// RegistrySkillRef is a parsed versioned registry skill reference.
type RegistrySkillRef struct {
	Scope   string
	Name    string
	Version string
	Ref     string
}

// ParseRegistrySkillRef parses @scope/name[:version] skill references.
func ParseRegistrySkillRef(raw string) (RegistrySkillRef, bool) {
	value := strings.TrimSpace(strings.TrimSuffix(raw, "*"))
	value = strings.TrimSpace(strings.TrimPrefix(value, "skill:"))
	if !strings.HasPrefix(value, "@") {
		return RegistrySkillRef{}, false
	}
	scoped := strings.TrimPrefix(value, "@")
	scope, rest, ok := strings.Cut(scoped, "/")
	if !ok {
		return RegistrySkillRef{}, false
	}
	name, version, hasVersion := strings.Cut(rest, ":")
	if !dnsRE.MatchString(scope) || !dnsRE.MatchString(name) {
		return RegistrySkillRef{}, false
	}
	if hasVersion && version == "" {
		return RegistrySkillRef{}, false
	}
	if version != "" && !IsSemver(version) {
		return RegistrySkillRef{}, false
	}
	ref := "@" + scope + "/" + name
	if version != "" {
		ref += ":" + version
	}
	return RegistrySkillRef{
		Scope:   scope,
		Name:    name,
		Version: version,
		Ref:     ref,
	}, true
}

// RegistrySkillsDir returns the persistent cache used for downloaded skills.
func RegistrySkillsDir() string {
	if dir := strings.TrimSpace(os.Getenv(RegistrySkillsDirEnv)); dir != "" {
		return dir
	}
	cache, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cache) == "" {
		return ""
	}
	return filepath.Join(cache, "telos", "skills")
}

// RegistrySkillPath returns the cache path for an exact registry skill version.
func RegistrySkillPath(ref RegistrySkillRef) string {
	if ref.Scope == "" || ref.Name == "" || ref.Version == "" {
		return ""
	}
	root := RegistrySkillsDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, ref.Scope, ref.Name, ref.Version)
}

// RegistrySkillRefs returns all registry skills declared by a spec and its
// local extends chain.
func RegistrySkillRefs(specPath string) ([]RegistrySkillRef, error) {
	seenSpecs := map[string]bool{}
	seenRefs := map[string]bool{}
	var refs []RegistrySkillRef
	var visit func(string) error
	visit = func(path string) error {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if seenSpecs[abs] {
			return nil
		}
		seenSpecs[abs] = true
		data, err := os.ReadFile(abs)
		if err != nil {
			return fmt.Errorf("read spec %s: %w", abs, err)
		}
		raw, _, ok := ParseFrontmatter(string(data))
		if !ok {
			return fmt.Errorf("%s has no valid YAML frontmatter", abs)
		}
		for _, value := range rawSkillValues(raw["skills"]) {
			ref, ok := ParseRegistrySkillRef(value)
			if !ok || seenRefs[ref.Ref] {
				continue
			}
			seenRefs[ref.Ref] = true
			refs = append(refs, ref)
		}
		if value, ok := raw["extends"]; ok {
			parent, err := resolvePath(filepath.Dir(abs), fmt.Sprint(value))
			if err != nil {
				return fmt.Errorf("'extends' points to non-existent path: %s", err)
			}
			return visit(parent)
		}
		return nil
	}
	if err := visit(specPath); err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Ref < refs[j].Ref })
	return refs, nil
}

func rawSkillValues(value any) []string {
	switch raw := value.(type) {
	case string:
		return []string{raw}
	case []interface{}:
		values := make([]string, 0, len(raw))
		for _, item := range raw {
			values = append(values, fmt.Sprint(item))
		}
		return values
	default:
		return nil
	}
}

func resolveCachedRegistrySkill(raw string) (string, bool, error) {
	ref, ok := ParseRegistrySkillRef(raw)
	if !ok || ref.Version == "" {
		return "", false, nil
	}
	path := RegistrySkillPath(ref)
	if path == "" {
		return "", false, nil
	}
	exists, err := skillDirExists(path)
	if err != nil || !exists {
		return "", false, err
	}
	return path, true, nil
}
