// Package spec handles SPEC.md loading, frontmatter parsing, skill resolution,
// compilation, and prompt rendering. It is the Go equivalent of the Python
// telos.spec package.
package spec

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// -- Regexps ------------------------------------------------------------------

var (
	frontmatterRE = regexp.MustCompile(`(?s)\A---\s*\n(.*?\n)---\s*\n?(.*)`)
	dnsRE         = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	durationRE    = regexp.MustCompile(`^(\d+)(s|m|h)$`)
	semverRE      = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
)

var durationUnits = map[string]int{"s": 1, "m": 60, "h": 3600}

// -- Environment spec (parsed frontmatter + body) ----------------------------

// EnvironmentSpec is the parsed public contract from a SPEC.md file.
type EnvironmentSpec struct {
	Path                       string
	Version                    string
	Name                       string
	ExtendsPath                string
	SkillPaths                 []string // nil means "not declared"
	SkillSourceRefs            map[string]string
	SpecText                   string
	IntervalSeconds            *int
	Tags                       []string
	Platform                   string // "local" or "cloud"
	RequiredVerifierSkillPaths []string
}

// ParseFrontmatter extracts YAML frontmatter and markdown body from text.
// Returns nil, "" if no frontmatter is found.
func ParseFrontmatter(text string) (map[string]interface{}, string, bool) {
	m := frontmatterRE.FindStringSubmatch(text)
	if m == nil {
		return nil, "", false
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal([]byte(m[1]), &raw); err != nil {
		return nil, "", false
	}
	if raw == nil {
		return nil, "", false
	}
	return raw, m[2], true
}

// LoadEnvironment loads and validates a SPEC.md file. Relative `extends` and
// `skills` paths resolve against the spec's own directory.
func LoadEnvironment(specPath string) (*EnvironmentSpec, error) {
	return LoadEnvironmentWithBase(specPath, "")
}

// LoadEnvironmentWithBase is like LoadEnvironment but resolves relative
// `extends` and `skills` paths against baseDir instead of the spec's own
// directory. An empty baseDir falls back to the spec's directory. This is
// used by the session runner when the spec has been copied into a session
// directory but its relative references must still resolve against the
// original location on disk.
func LoadEnvironmentWithBase(specPath string, baseDir string) (*EnvironmentSpec, error) {
	absPath, err := filepath.Abs(specPath)
	if err != nil {
		return nil, err
	}
	if filepath.Ext(absPath) != ".md" {
		return nil, fmt.Errorf("%s: only markdown specs with frontmatter are supported", absPath)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("spec file not found: %s", absPath)
	}
	raw, body, ok := ParseFrontmatter(string(data))
	if !ok {
		return nil, fmt.Errorf("%s has no valid YAML frontmatter", absPath)
	}
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Dir(absPath)
	} else {
		abs, err := filepath.Abs(baseDir)
		if err != nil {
			return nil, fmt.Errorf("resolve spec base dir: %w", err)
		}
		baseDir = abs
	}
	return parseEnvFields(raw, absPath, baseDir, body)
}

func parseEnvFields(raw map[string]interface{}, path, baseDir, body string) (*EnvironmentSpec, error) {
	version := strings.TrimSpace(requireStr(raw, "version", path))
	if version == "" {
		return nil, fmt.Errorf("%s: missing required field 'version'", path)
	}
	if !semverRE.MatchString(version) {
		return nil, fmt.Errorf("%s: version must be semver like 0.1.0", path)
	}
	if strings.TrimSpace(requireStr(raw, "package_version", path)) != "" {
		return nil, fmt.Errorf("%s: package_version is no longer supported; use version", path)
	}
	name := requireStr(raw, "name", path)
	if name == "" {
		return nil, fmt.Errorf("%s: missing required field 'name'", path)
	}
	if !dnsRE.MatchString(name) {
		return nil, fmt.Errorf("invalid name '%s': must be lowercase DNS-compatible", name)
	}
	specBody := strings.TrimSpace(body)
	if specBody == "" {
		return nil, fmt.Errorf("%s: spec body is empty", path)
	}

	env := &EnvironmentSpec{
		Path:     path,
		Version:  version,
		Name:     name,
		SpecText: specBody,
	}

	// extends
	if v, ok := raw["extends"]; ok {
		resolved, err := resolvePath(baseDir, fmt.Sprint(v))
		if err != nil {
			return nil, fmt.Errorf("'extends' points to non-existent path: %s", err)
		}
		env.ExtendsPath = resolved
	}

	// platform
	if v, ok := raw["platform"]; ok {
		p := fmt.Sprint(v)
		if p != "local" && p != "cloud" {
			return nil, fmt.Errorf("%s: invalid platform '%s' (valid: cloud, local)", path, p)
		}
		env.Platform = p
	}

	// skills
	if v, ok := raw["skills"]; ok {
		refs, reqPaths, sourceRefs, err := parseSkillRefs(baseDir, v)
		if err != nil {
			return nil, err
		}
		env.SkillPaths = refs
		env.RequiredVerifierSkillPaths = reqPaths
		env.SkillSourceRefs = sourceRefs
	}

	// interval
	if v, ok := raw["interval"]; ok {
		secs, err := parseDuration(fmt.Sprint(v), path)
		if err != nil {
			return nil, err
		}
		env.IntervalSeconds = &secs
	}

	// tags
	if v, ok := raw["tags"]; ok {
		list, ok := v.([]interface{})
		if !ok {
			return nil, fmt.Errorf("%s: 'tags' must be a list", path)
		}
		for _, t := range list {
			env.Tags = append(env.Tags, fmt.Sprint(t))
		}
	}

	return env, nil
}

func parseSkillRefs(baseDir string, v interface{}) (paths []string, required []string, sourceRefs map[string]string, err error) {
	var items []string
	switch val := v.(type) {
	case string:
		items = []string{val}
	case []interface{}:
		for _, item := range val {
			items = append(items, fmt.Sprint(item))
		}
	default:
		return nil, nil, nil, fmt.Errorf("'skills' must be a path or a list of paths")
	}

	sourceRefs = map[string]string{}
	for _, raw := range items {
		raw = strings.TrimSpace(raw)
		isRequired := strings.HasSuffix(raw, "*")
		if isRequired {
			raw = strings.TrimSpace(strings.TrimSuffix(raw, "*"))
		}
		if raw == "" {
			return nil, nil, nil, fmt.Errorf("'skills' contains an empty skill reference")
		}
		resolved, err := resolveSkillPath(baseDir, raw)
		if err != nil {
			return nil, nil, nil, err
		}
		paths = append(paths, resolved)
		if isScopedRegistrySkillRef(raw) {
			sourceRefs[resolved] = raw
		}
		if isRequired {
			required = append(required, resolved)
		}
	}
	return paths, required, sourceRefs, nil
}

func resolvePath(baseDir, raw string) (string, error) {
	p := raw
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err == nil {
		return abs, nil
	}
	if filepath.Base(abs) == "spec.md" {
		canon := filepath.Join(filepath.Dir(abs), "SPEC.md")
		if _, err := os.Stat(canon); err == nil {
			return canon, nil
		}
	}
	return "", fmt.Errorf("%s", abs)
}

func resolveSkillPath(baseDir, raw string) (string, error) {
	if isScopedRegistrySkillRef(raw) {
		if path, ok := resolvePackageLocalRegistrySkill(baseDir, raw); ok {
			return path, nil
		}
		if path, ok, err := resolvePlatformCatalogueSkill(raw); err != nil {
			return "", err
		} else if ok {
			return path, nil
		}
		return "", fmt.Errorf("'skills' registry ref %q cannot resolve to local skills outside the Telos platform catalogue or a packaged Telos lockfile", raw)
	}
	for _, candidate := range skillPathCandidates(raw) {
		local := candidate
		if !filepath.IsAbs(local) {
			local = filepath.Join(baseDir, local)
		}
		abs, err := filepath.Abs(local)
		if err != nil {
			return "", err
		}
		if ok, err := skillDirExists(abs); err != nil {
			return "", err
		} else if ok {
			return abs, nil
		}
		if !filepath.IsAbs(candidate) {
			packageLocal := filepath.Join(baseDir, "skills", candidate)
			if ok, err := skillDirExists(packageLocal); err != nil {
				return "", err
			} else if ok {
				return packageLocal, nil
			}
		}
		// Try the default external skill catalogue.
		defaultDir := DefaultSkillsDir()
		if defaultDir != "" {
			catalog := filepath.Join(defaultDir, candidate)
			if ok, err := skillDirExists(catalog); err != nil {
				return "", err
			} else if ok {
				return catalog, nil
			}
		}
	}
	return "", fmt.Errorf("'skills' references unknown skill or path '%s'", raw)
}

func resolvePlatformCatalogueSkill(raw string) (string, bool, error) {
	scope, name, ok := registrySkillParts(raw)
	if !ok || !isPlatformSkillScope(scope) {
		return "", false, nil
	}
	defaultDir := DefaultSkillsDir()
	if defaultDir == "" {
		return "", false, nil
	}
	path := filepath.Join(defaultDir, name)
	exists, err := skillDirExists(path)
	if err != nil || !exists {
		return "", false, err
	}
	return path, true, nil
}

func resolvePackageLocalRegistrySkill(baseDir, raw string) (string, bool) {
	_, _, hasPackageManifest := packageManifestSkillPaths(baseDir)
	if !hasPackageManifest {
		return "", false
	}
	name := registrySkillLocalName(raw)
	if name == "" {
		return "", false
	}
	path := filepath.Join(baseDir, "skills", name)
	ok, err := skillDirExists(path)
	return path, err == nil && ok
}

func registrySkillParts(raw string) (string, string, bool) {
	value := strings.TrimSpace(strings.TrimSuffix(raw, "*"))
	value = strings.TrimSpace(strings.TrimPrefix(value, "skill:"))
	if !strings.HasPrefix(value, "@") {
		return "", "", false
	}
	scoped := strings.TrimPrefix(value, "@")
	scope, rest, ok := strings.Cut(scoped, "/")
	if !ok {
		return "", "", false
	}
	name, _, _ := strings.Cut(rest, ":")
	if !dnsRE.MatchString(scope) || !dnsRE.MatchString(name) {
		return "", "", false
	}
	return scope, name, true
}

func isPlatformSkillScope(scope string) bool {
	return scope == "telos"
}

func skillDirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func skillPathCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	candidates := []string{raw}
	alias := registrySkillLocalName(raw)
	if alias != "" && alias != raw {
		candidates = append(candidates, alias)
	}
	return candidates
}

func isScopedRegistrySkillRef(raw string) bool {
	_, _, ok := registrySkillParts(raw)
	return ok
}

func registrySkillLocalName(raw string) string {
	if _, name, ok := registrySkillParts(raw); ok {
		return name
	}
	value := strings.TrimSpace(strings.TrimSuffix(raw, "*"))
	value = strings.TrimPrefix(value, "skill:")
	if name, _, ok := strings.Cut(value, ":"); ok {
		value = name
	}
	if dnsRE.MatchString(value) {
		return value
	}
	return ""
}

func requireStr(raw map[string]interface{}, key, ctx string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

func parseDuration(raw, ctx string) (int, error) {
	m := durationRE.FindStringSubmatch(raw)
	if m == nil {
		return 0, fmt.Errorf("%s: invalid 'interval' value '%s'", ctx, raw)
	}
	val := 0
	fmt.Sscanf(m[1], "%d", &val)
	if val <= 0 {
		return 0, fmt.Errorf("%s: 'interval' must be positive", ctx)
	}
	return val * durationUnits[m[2]], nil
}

// -- Content hash -------------------------------------------------------------

func sha256str(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func skillFingerprint(s *Skill) string {
	parts := []string{s.Name, s.Instructions}
	scripts := make([]*SkillScript, len(s.Scripts))
	copy(scripts, s.Scripts)
	sort.Slice(scripts, func(i, j int) bool { return scripts[i].Name < scripts[j].Name })
	for _, sc := range scripts {
		parts = append(parts, sc.Name, sc.Language, sc.Content)
	}
	return sha256str(parts...)
}

func merkleHash(env *EnvironmentSpec, extendsCompiled *CompiledEnvironment, skills []*Skill) (string, error) {
	specData, err := os.ReadFile(env.Path)
	if err != nil {
		return "", fmt.Errorf("read spec for content hash: %w", err)
	}
	parts := []string{env.SpecText, string(specData)}
	if extendsCompiled != nil {
		parts = append(parts, extendsCompiled.ContentHash)
	}
	sorted := make([]*Skill, len(skills))
	copy(sorted, skills)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, s := range sorted {
		parts = append(parts, s.Name, skillFingerprint(s))
	}
	return sha256str(parts...), nil
}
