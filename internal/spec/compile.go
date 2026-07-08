package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CompiledEnvironment is the fully resolved environment plan.
type CompiledEnvironment struct {
	Environment            *EnvironmentSpec
	SpecText               string
	Namespace              string
	Cluster                string
	Context                string
	Lineage                []string
	ExtendsCompiled        *CompiledEnvironment
	Skills                 []*Skill
	RequiredVerifierSkills []*Skill
	ContentHash            string
}

// CompileEnvironment compiles a spec file into a fully resolved plan.
func CompileEnvironment(specPath string) (*CompiledEnvironment, error) {
	return CompileEnvironmentWithBase(specPath, "")
}

// CompileEnvironmentWithBase is like CompileEnvironment but resolves the
// root spec's relative `extends` and `skills` paths against baseDir. An
// empty baseDir falls back to the spec's directory. Transitively `extends`-d
// specs continue to resolve relative to their own directories.
func CompileEnvironmentWithBase(specPath string, baseDir string) (*CompiledEnvironment, error) {
	abs, err := filepath.Abs(specPath)
	if err != nil {
		return nil, err
	}
	return compileEnv(abs, baseDir, nil)
}

func compileEnv(envPath string, baseDir string, visited map[string]bool) (*CompiledEnvironment, error) {
	if visited == nil {
		visited = map[string]bool{}
	}
	if visited[envPath] {
		return nil, fmt.Errorf("cycle detected: %s already in compilation chain", envPath)
	}
	visited[envPath] = true

	compileBaseDir, err := resolvedCompileBaseDir(envPath, baseDir)
	if err != nil {
		return nil, err
	}

	env, err := LoadEnvironmentWithBase(envPath, baseDir)
	if err != nil {
		return nil, err
	}

	var extendsCompiled *CompiledEnvironment
	if env.ExtendsPath != "" {
		extendsCompiled, err = compileEnv(env.ExtendsPath, "", visited)
		if err != nil {
			return nil, err
		}
	}

	namespace := fmt.Sprintf("ns-%s", env.Name)
	if extendsCompiled != nil {
		namespace = extendsCompiled.Namespace
	}
	cluster := "telos"
	context := cluster
	lineage := computeLineage(namespace, extendsCompiled)

	packageSkillPaths, packageRequiredPaths, hasPackageManifest := packageManifestSkillPaths(compileBaseDir)

	// Resolve skills
	var declared []*Skill
	skillPaths := appendMissingPaths(env.SkillPaths, packageSkillPaths)
	if skillPaths != nil {
		declared, err = ResolveSkillsFromPaths(skillPaths)
		if err != nil {
			return nil, err
		}
		annotateSkillSourceRefs(declared, env.SkillSourceRefs)
	}
	var required []*Skill
	requiredPaths := appendMissingPaths(env.RequiredVerifierSkillPaths, packageRequiredPaths)
	if len(requiredPaths) > 0 {
		required, err = ResolveSkillsFromPaths(requiredPaths)
		if err != nil {
			return nil, err
		}
		annotateSkillSourceRefs(required, env.SkillSourceRefs)
	}
	var verifier []*Skill
	if !hasPackageManifest {
		verifier = ResolveDefaultVerifierSkills()
	}

	// Merge: declared + required + verifier, dedup by name
	byName := map[string]*Skill{}
	var order []string
	for _, list := range [][]*Skill{declared, required, verifier} {
		for _, s := range list {
			if _, exists := byName[s.Name]; !exists {
				byName[s.Name] = s
				order = append(order, s.Name)
			}
		}
	}
	var skills []*Skill
	for _, n := range order {
		skills = append(skills, byName[n])
	}

	// Required verifier skills
	requiredNames := map[string]bool{}
	for _, s := range required {
		requiredNames[s.Name] = true
	}
	var reqVerifierSkills []*Skill
	for _, s := range skills {
		if requiredNames[s.Name] {
			reqVerifierSkills = append(reqVerifierSkills, s)
		}
	}

	contentHash, err := merkleHash(env, extendsCompiled, skills)
	if err != nil {
		return nil, err
	}

	return &CompiledEnvironment{
		Environment:            env,
		SpecText:               env.SpecText,
		Namespace:              namespace,
		Cluster:                cluster,
		Context:                context,
		Lineage:                lineage,
		ExtendsCompiled:        extendsCompiled,
		Skills:                 skills,
		RequiredVerifierSkills: reqVerifierSkills,
		ContentHash:            contentHash,
	}, nil
}

func annotateSkillSourceRefs(skills []*Skill, refs map[string]string) {
	if len(refs) == 0 {
		return
	}
	for _, skill := range skills {
		if skill == nil || strings.TrimSpace(skill.Path) == "" {
			continue
		}
		if ref, ok := refs[skill.Path]; ok {
			skill.SourceRef = ref
		}
	}
}

func resolvedCompileBaseDir(envPath string, baseDir string) (string, error) {
	if baseDir == "" {
		return filepath.Dir(envPath), nil
	}
	return filepath.Abs(baseDir)
}

func appendMissingPaths(base []string, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	out := append([]string{}, base...)
	seen := map[string]bool{}
	for _, path := range out {
		if abs, err := filepath.Abs(path); err == nil {
			seen[abs] = true
		}
	}
	for _, path := range extra {
		abs, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

func packageManifestSkillPaths(baseDir string) ([]string, []string, bool) {
	manifestPath := filepath.Join(baseDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, false
	}
	var manifest ApplyPackageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, nil, false
	}
	if !isApplyPackageManifest(manifest) {
		return nil, nil, false
	}
	var paths []string
	for name := range manifest.Skills {
		if !dnsRE.MatchString(name) {
			continue
		}
		path := filepath.Join(baseDir, "skills", name)
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
			continue
		}
		paths = append(paths, path)
	}
	return paths, nil, true
}

func isApplyPackageManifest(manifest ApplyPackageManifest) bool {
	return manifest.SchemaVersion == ApplyPackageSchemaVersion &&
		strings.TrimSpace(manifest.Spec.Digest) != "" &&
		manifest.Skills != nil
}

func computeLineage(namespace string, extendsCompiled *CompiledEnvironment) []string {
	seen := map[string]bool{namespace: true}
	lineage := []string{namespace}
	if extendsCompiled == nil {
		return lineage
	}
	for _, ns := range extendsCompiled.Lineage {
		if seen[ns] {
			continue
		}
		seen[ns] = true
		lineage = append(lineage, ns)
	}
	return lineage
}

// ToIRJSON serializes a compiled environment to the inspectable IR format.
func ToIRJSON(c *CompiledEnvironment) map[string]interface{} {
	var skillList []map[string]interface{}
	for _, s := range c.Skills {
		skillList = append(skillList, map[string]interface{}{
			"name":        s.Name,
			"description": s.Description,
			"scripts":     scriptNames(s),
		})
	}
	specText := c.SpecText
	if len(specText) > 500 {
		specText = specText[:500]
	}
	platform := c.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	var extends any
	if c.ExtendsCompiled != nil {
		extends = map[string]interface{}{
			"name":         c.ExtendsCompiled.Environment.Name,
			"path":         c.Environment.ExtendsPath,
			"namespace":    c.ExtendsCompiled.Namespace,
			"content_hash": c.ExtendsCompiled.ContentHash,
		}
	}
	return map[string]interface{}{
		"kind":             "telos.compiled_environment.v1",
		"name":             c.Environment.Name,
		"spec":             specText,
		"namespace":        c.Namespace,
		"cluster":          c.Cluster,
		"context":          c.Context,
		"lineage":          c.Lineage,
		"extends":          extends,
		"interval_seconds": c.Environment.IntervalSeconds,
		"tags":             c.Environment.Tags,
		"platform":         platform,
		"skills":           skillList,
	}
}

func scriptNames(s *Skill) []string {
	var names []string
	for _, sc := range s.Scripts {
		names = append(names, sc.Name)
	}
	return names
}
