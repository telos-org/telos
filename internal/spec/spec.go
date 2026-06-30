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
)

var durationUnits = map[string]int{"s": 1, "m": 60, "h": 3600}

// -- Environment spec (parsed frontmatter + body) ----------------------------

// EnvironmentSpec is the parsed v0 public contract from a SPEC.md file.
type EnvironmentSpec struct {
	Path                       string
	Version                    string
	PackageVersion             string
	Name                       string
	ExtendsPath                string
	SkillPaths                 []string // nil means "not declared"
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
	frontmatterVersion := requireStr(raw, "version", path)
	schemaVersion := requireStr(raw, "schema", path)
	packageVersion := requireStr(raw, "package_version", path)
	if schemaVersion == "" {
		if frontmatterVersion == "" {
			return nil, fmt.Errorf("%s: missing required field 'version'", path)
		}
		if frontmatterVersion == "v0" {
			schemaVersion = frontmatterVersion
		} else {
			schemaVersion = "v0"
			if packageVersion == "" {
				packageVersion = frontmatterVersion
			}
		}
	} else if packageVersion == "" && frontmatterVersion != "" && frontmatterVersion != schemaVersion {
		packageVersion = frontmatterVersion
	}
	if schemaVersion != "v0" {
		return nil, fmt.Errorf("unsupported schema '%s' (only 'v0' is valid)", schemaVersion)
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
		Path:           path,
		Version:        schemaVersion,
		PackageVersion: packageVersion,
		Name:           name,
		SpecText:       specBody,
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
		refs, reqPaths, err := parseSkillRefs(baseDir, v)
		if err != nil {
			return nil, err
		}
		env.SkillPaths = refs
		env.RequiredVerifierSkillPaths = reqPaths
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

func parseSkillRefs(baseDir string, v interface{}) (paths []string, required []string, err error) {
	var items []string
	switch val := v.(type) {
	case string:
		items = []string{val}
	case []interface{}:
		for _, item := range val {
			items = append(items, fmt.Sprint(item))
		}
	default:
		return nil, nil, fmt.Errorf("'skills' must be a path or a list of paths")
	}

	for _, raw := range items {
		raw = strings.TrimSpace(raw)
		isRequired := strings.HasSuffix(raw, "*")
		if isRequired {
			raw = strings.TrimSpace(strings.TrimSuffix(raw, "*"))
		}
		if raw == "" {
			return nil, nil, fmt.Errorf("'skills' contains an empty skill reference")
		}
		resolved, err := resolveSkillPath(baseDir, raw)
		if err != nil {
			return nil, nil, err
		}
		paths = append(paths, resolved)
		if isRequired {
			required = append(required, resolved)
		}
	}
	return paths, required, nil
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
	local := raw
	if !filepath.IsAbs(local) {
		local = filepath.Join(baseDir, local)
	}
	abs, err := filepath.Abs(local)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err == nil {
		return abs, nil
	}
	if !filepath.IsAbs(raw) {
		packageLocal := filepath.Join(baseDir, "skills", raw)
		if _, err := os.Stat(packageLocal); err == nil {
			return packageLocal, nil
		}
	}
	// Try the default external skill catalogue.
	defaultDir := DefaultSkillsDir()
	if defaultDir != "" {
		catalog := filepath.Join(defaultDir, raw)
		if _, err := os.Stat(catalog); err == nil {
			return catalog, nil
		}
	}
	return "", fmt.Errorf("'skills' references unknown skill or path '%s'", raw)
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
