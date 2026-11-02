package spec

import (
	"strings"
	"text/template"
)

// Role is the internal agent role.
type Role = string

const (
	RoleProver   Role = "prover"
	RoleVerifier Role = "verifier"
)

var taskTemplate = template.Must(template.ParseFS(promptsFS, "prompts/task.md.tmpl"))

type taskPromptData struct {
	RolePrompt     string
	Spec           *EnvironmentSpec
	Body           string
	Skills         []taskPromptSkill
	TranscriptPath string
	VerifyRubrics  bool
}

type taskPromptSkill struct {
	Name        string
	Description string
	Required    bool
}

// RenderTask builds the complete task prompt for one agent role.
func RenderTask(role Role, compiled *CompiledEnvironment, transcriptPath string) string {
	rolePrompt, err := ReadPrompt(role + ".md")
	if err != nil {
		panic(err)
	}
	data := taskPromptData{
		RolePrompt:     rolePrompt,
		Spec:           compiled.Environment,
		Body:           compiled.SpecText,
		TranscriptPath: strings.TrimSpace(transcriptPath),
		VerifyRubrics:  role == RoleVerifier && len(compiled.RequiredVerifierSkills) > 0,
	}
	requiredNames := map[string]bool{}
	for _, skill := range compiled.RequiredVerifierSkills {
		requiredNames[skill.Name] = true
	}
	for _, skill := range compiled.Skills {
		data.Skills = append(data.Skills, taskPromptSkill{
			Name:        skill.Name,
			Description: strings.TrimSpace(skill.Description),
			Required:    requiredNames[skill.Name],
		})
	}
	var prompt strings.Builder
	if err := taskTemplate.Execute(&prompt, data); err != nil {
		panic(err)
	}
	return strings.TrimSpace(prompt.String())
}
