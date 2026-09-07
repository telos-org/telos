package spec

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Package skills are relocated to skills/<name>; authored filesystem paths
// cannot be carried into the package's SPEC.md unchanged.
func portablePackageSpec(data []byte, compiled *CompiledEnvironment, skillRefs map[string]string) ([]byte, error) {
	if len(compiled.Skills) == 0 {
		return data, nil
	}
	match := frontmatterRE.FindSubmatchIndex(data)
	if match == nil {
		return nil, fmt.Errorf("package root spec has no YAML frontmatter")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data[match[2]:match[3]], &document); err != nil {
		return nil, fmt.Errorf("parse package root spec: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("package root spec frontmatter must be a mapping")
	}
	required := make(map[string]bool, len(compiled.RequiredVerifierSkills))
	for _, skill := range compiled.RequiredVerifierSkills {
		if skill != nil {
			required[skill.Name] = true
		}
	}
	refs := make([]string, 0, len(compiled.Skills))
	for _, skill := range compiled.Skills {
		if skill == nil {
			return nil, fmt.Errorf("skill is required")
		}
		ref := "./skills/" + skill.Name
		if skillRefs != nil {
			ref = skillRefs[skill.Name]
		}
		if required[skill.Name] {
			ref += "*"
		}
		refs = append(refs, ref)
	}
	var skills yaml.Node
	if err := skills.Encode(refs); err != nil {
		return nil, fmt.Errorf("encode package skill references: %w", err)
	}
	root := document.Content[0]
	replaced := false
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "skills" {
			root.Content[i+1] = &skills
			replaced = true
			break
		}
	}
	if !replaced {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "skills"}, &skills)
	}
	header, err := yaml.Marshal(&document)
	if err != nil {
		return nil, fmt.Errorf("encode package root spec: %w", err)
	}
	out := append([]byte(nil), data[:match[2]]...)
	out = append(out, header...)
	return append(out, data[match[3]:]...), nil
}
