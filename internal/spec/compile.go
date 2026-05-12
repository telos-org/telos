package spec

import (
	"fmt"
	"path/filepath"
)

// CompiledEnvironment is the fully resolved environment plan.
type CompiledEnvironment struct {
	Environment            *EnvironmentSpec
	SpecText               string
	Namespace              string
	Cluster                string
	Context                string
	Lineage                []string
	Skills                 []*Skill
	RequiredVerifierSkills []*Skill
	ContentHash            string
}

// CompileEnvironment compiles a spec file into a fully resolved plan.
func CompileEnvironment(specPath string) (*CompiledEnvironment, error) {
	abs, err := filepath.Abs(specPath)
	if err != nil {
		return nil, err
	}
	return compileEnv(abs, nil)
}

func compileEnv(envPath string, visited map[string]bool) (*CompiledEnvironment, error) {
	if visited == nil {
		visited = map[string]bool{}
	}
	if visited[envPath] {
		return nil, fmt.Errorf("cycle detected: %s already in compilation chain", envPath)
	}
	visited[envPath] = true

	env, err := LoadEnvironment(envPath)
	if err != nil {
		return nil, err
	}

	namespace := fmt.Sprintf("ns-%s", env.Name)
	cluster := "telos"
	context := cluster

	// Resolve skills
	var declared []*Skill
	if env.SkillPaths != nil {
		declared, err = ResolveSkillsFromPaths(env.SkillPaths)
		if err != nil {
			return nil, err
		}
	}
	var required []*Skill
	if len(env.RequiredVerifierSkillPaths) > 0 {
		required, err = ResolveSkillsFromPaths(env.RequiredVerifierSkillPaths)
		if err != nil {
			return nil, err
		}
	}
	verifier := ResolveVerifierSkills()

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

	contentHash := merkleHash(env, skills)

	return &CompiledEnvironment{
		Environment:            env,
		SpecText:               env.SpecText,
		Namespace:              namespace,
		Cluster:                cluster,
		Context:                context,
		Lineage:                []string{namespace},
		Skills:                 skills,
		RequiredVerifierSkills: reqVerifierSkills,
		ContentHash:            contentHash,
	}, nil
}

// ToIRJSON serializes a compiled environment to the inspectable IR format.
func ToIRJSON(c *CompiledEnvironment) map[string]interface{} {
	requiredNames := map[string]bool{}
	for _, s := range c.RequiredVerifierSkills {
		requiredNames[s.Name] = true
	}
	var skillList []map[string]interface{}
	for _, s := range c.Skills {
		skillList = append(skillList, map[string]interface{}{
			"name":              s.Name,
			"description":       s.Description,
			"scripts":           scriptNames(s),
			"required_verifier": requiredNames[s.Name],
		})
	}
	var reqNames []string
	for _, s := range c.RequiredVerifierSkills {
		reqNames = append(reqNames, s.Name)
	}
	specText := c.SpecText
	if len(specText) > 500 {
		specText = specText[:500]
	}
	platform := c.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	return map[string]interface{}{
		"kind":                     "telos.compiled_environment.v1",
		"name":                     c.Environment.Name,
		"spec":                     specText,
		"namespace":                c.Namespace,
		"cluster":                  c.Cluster,
		"context":                  c.Context,
		"lineage":                  c.Lineage,
		"extends":                  nil,
		"interval_seconds":         c.Environment.IntervalSeconds,
		"tags":                     c.Environment.Tags,
		"platform":                 platform,
		"skills":                   skillList,
		"required_verifier_skills": reqNames,
	}
}

func scriptNames(s *Skill) []string {
	var names []string
	for _, sc := range s.Scripts {
		names = append(names, sc.Name)
	}
	return names
}
