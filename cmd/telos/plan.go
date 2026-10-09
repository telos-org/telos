package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/spec"
)

// -- plan ---------------------------------------------------------------------

type specComparison struct {
	sessionID  string
	currentRef string
	diff       string
	current    planSpecState
	proposed   planSpecState
}

type planSpecState struct {
	Version         string          `json:"version,omitempty"`
	IntervalSeconds *int            `json:"interval_seconds,omitempty"`
	Skills          []planSkillLock `json:"skills"`
}

type planSkillLock struct {
	Name    string `json:"name"`
	Ref     string `json:"ref,omitempty"`
	Digest  string `json:"digest"`
	Starred bool   `json:"required_rubric,omitempty"`
}

func cmdPlan(args []string) {
	fs := newCommandFlagSet("plan", "telos plan SPEC.md [flags]")
	sessionID := fs.String("goal", "", "ID of the Goal to compare against")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	requireArgCount(fs, 1, "one SPEC.md")
	specPath := resolveSpecPath(fs.Arg(0))
	proposedSpec, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	compiled, proposedState, err := compilePlanSpec(specPath, contextOverride, proposedSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var comparison *specComparison
	if strings.TrimSpace(*sessionID) != "" {
		comparison, err = compareSessionSpec(
			*sessionID,
			proposedSpec,
			proposedState,
			contextOverride,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	targetContext := strings.TrimSpace(contextOverride)
	if targetContext == "" {
		targetContext = strings.TrimSpace(cfg.Context)
	}
	if targetContext == "" {
		targetContext = "personal"
	}
	if cfg.AuthToken != "" {
		control, err := cloud.ControlClientForContext(contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		targetContext = control.ContextName()
		if comparison == nil {
			if err := refuseDuplicateGoal(control, "plan", fs.Arg(0), compiled.Environment.Name, contextOverride); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
		}
	}
	targetOperation := "create"
	if comparison != nil {
		targetOperation = "update"
	}
	targetScope := map[string]interface{}{
		"mode":      "cloud",
		"operation": targetOperation,
		"context":   targetContext,
	}
	plan := map[string]interface{}{
		"spec": map[string]interface{}{
			"name":         compiled.Environment.Name,
			"path":         specPath,
			"content_hash": compiled.ContentHash,
			"namespace":    compiled.Namespace,
			"skills":       skillNames(compiled.Skills),
			"required_rubrics": skillNames(
				compiled.RequiredVerifierSkills,
			),
		},
		"goal": map[string]interface{}{
			"interval_seconds": compiled.Environment.IntervalSeconds,
		},
		"target": targetScope,
	}
	if comparison != nil {
		plan["change"] = map[string]interface{}{
			"goal_id":     comparison.sessionID,
			"current_ref": comparison.currentRef,
			"current":     comparison.current,
			"proposed":    comparison.proposed,
			"spec_diff":   comparison.diff,
		}
	}

	if *jsonOut {
		printJSON(plan)
		return
	}

	printPlanPreview(os.Stdout, compiled, targetContext, comparison)
}

func compilePlanSpec(
	specPath string,
	contextOverride string,
	markdown []byte,
) (*spec.CompiledEnvironment, planSpecState, error) {
	var compiled *spec.CompiledEnvironment
	var state planSpecState
	err := withPlanRegistrySkills(specPath, contextOverride, func() error {
		var err error
		compiled, err = spec.CompileEnvironment(specPath)
		if err != nil {
			return err
		}
		state, err = planSpecStateForCompiled(compiled, markdown)
		if err != nil {
			return fmt.Errorf("build plan metadata: %w", err)
		}
		return nil
	})
	return compiled, state, err
}

func printPlanPreview(
	out io.Writer,
	compiled *spec.CompiledEnvironment,
	contextName string,
	comparison *specComparison,
) {
	printSummaryField(out, "Spec", compiled.Environment.Name)
	// A plan against a Goal shows version and interval changes after the skills instead.
	if comparison == nil {
		printSummaryField(out, "Version", compiled.Environment.Version)
		if compiled.Environment.IntervalSeconds != nil {
			printSummaryField(out, "Interval", formatPlanInterval(compiled.Environment.IntervalSeconds))
		}
	}
	printSummaryField(out, "Context", contextName)
	if comparison != nil {
		printSummaryField(out, "Goal", comparison.sessionID)
		printSummaryField(out, "Current", comparison.currentRef)
	}
	if len(compiled.Skills) > 0 {
		printSummaryField(out, "Skills", strings.Join(skillDisplayNames(compiled), ", "))
	}
	if comparison == nil {
		return
	}
	printPlanStateDelta(out, comparison.current, comparison.proposed)
	fmt.Fprintln(out)
	if comparison.diff == "" {
		fmt.Fprintln(out, "No spec changes.")
		return
	}
	fmt.Fprint(out, comparison.diff)
	if !strings.HasSuffix(comparison.diff, "\n") {
		fmt.Fprintln(out)
	}
}

func compareSessionSpec(
	sessionID string,
	proposed []byte,
	proposedState planSpecState,
	contextOverride string,
) (*specComparison, error) {
	sessionID = strings.TrimSpace(sessionID)
	if isLocalApplyID(sessionID) {
		return nil, fmt.Errorf("%s is a local Goal; telos plan --goal only compares Telos Cloud Goals", sessionID)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, err
	}
	return compareCloudSessionSpecWithState(control, sessionID, proposed, proposedState)
}

func compareCloudSessionSpec(
	control *cloud.Client,
	sessionID string,
	proposed []byte,
) (*specComparison, error) {
	proposedState, err := planSpecStateFromMarkdown(proposed, nil)
	if err != nil {
		return nil, err
	}
	return compareCloudSessionSpecWithState(control, sessionID, proposed, proposedState)
}

func compareCloudSessionSpecWithState(
	control *cloud.Client,
	sessionID string,
	proposed []byte,
	proposedState planSpecState,
) (*specComparison, error) {
	pkg, err := packageForSession(control, sessionID)
	if err != nil {
		return nil, err
	}
	current, manifest, err := verifiedPackageContents(pkg)
	if err != nil {
		return nil, err
	}
	currentState, err := planSpecStateFromMarkdown(current, manifest)
	if err != nil {
		return nil, err
	}
	return newSpecComparisonWithStates(
		sessionID,
		pkg.reference.ref,
		current,
		proposed,
		currentState,
		proposedState,
	), nil
}

func newSpecComparison(sessionID string, currentRef string, current, proposed []byte) *specComparison {
	return newSpecComparisonWithStates(
		sessionID,
		currentRef,
		current,
		proposed,
		planSpecState{},
		planSpecState{},
	)
}

func newSpecComparisonWithStates(
	sessionID string,
	currentRef string,
	current []byte,
	proposed []byte,
	currentState planSpecState,
	proposedState planSpecState,
) *specComparison {
	return &specComparison{
		sessionID:  sessionID,
		currentRef: currentRef,
		current:    currentState,
		proposed:   proposedState,
		diff: string(internaldiff.Diff(
			"deployed/SPEC.md",
			current,
			"proposed/SPEC.md",
			proposed,
		)),
	}
}

func planSpecStateForCompiled(
	compiled *spec.CompiledEnvironment,
	markdown []byte,
) (planSpecState, error) {
	pkg, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		return planSpecState{}, err
	}
	return planSpecStateFromMarkdown(markdown, &pkg.Manifest)
}

func planSpecStateFromMarkdown(
	markdown []byte,
	manifest *spec.ApplyPackageManifest,
) (planSpecState, error) {
	raw, _, ok := spec.ParseFrontmatter(string(markdown))
	if !ok {
		return planSpecState{}, fmt.Errorf("spec has no valid YAML frontmatter")
	}
	state := planSpecState{Skills: []planSkillLock{}}
	if version, ok := raw["version"].(string); ok {
		state.Version = strings.TrimSpace(version)
	}
	if interval, ok := raw["interval"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(fmt.Sprint(interval)))
		if err != nil || duration < 0 || duration%time.Second != 0 {
			return planSpecState{}, fmt.Errorf("invalid interval %q", interval)
		}
		seconds := int(duration / time.Second)
		state.IntervalSeconds = &seconds
	}
	if manifest == nil {
		return state, nil
	}
	names := make([]string, 0, len(manifest.Skills))
	for name := range manifest.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lock := manifest.Skills[name]
		ref := strings.TrimSpace(lock.Ref)
		if ref == "" {
			ref = strings.TrimSpace(manifest.SkillProvenance[name].Ref)
		}
		state.Skills = append(state.Skills, planSkillLock{
			Name:    name,
			Ref:     ref,
			Digest:  strings.TrimSpace(lock.Digest),
			Starred: lock.Starred,
		})
	}
	return state, nil
}

func printPlanStateDelta(out io.Writer, current, proposed planSpecState) {
	currentInterval := formatPlanInterval(current.IntervalSeconds)
	proposedInterval := formatPlanInterval(proposed.IntervalSeconds)
	versionChanged := current.Version != proposed.Version
	intervalChanged := currentInterval != proposedInterval
	skillsChanged := !slices.Equal(current.Skills, proposed.Skills)
	if !versionChanged && !intervalChanged && !skillsChanged {
		return
	}
	if versionChanged {
		printSummaryField(out, "Version", planDeltaValue(current.Version, proposed.Version))
	}
	if intervalChanged {
		printSummaryField(out, "Interval", planDeltaValue(currentInterval, proposedInterval))
	}
	if skillsChanged {
		if versionChanged || intervalChanged {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, "Skill locks")
		printDetailField(out, "current", formatPlanSkillLocks(current.Skills))
		printDetailField(out, "proposed", formatPlanSkillLocks(proposed.Skills))
	}
}

func planDeltaValue(current, proposed string) string {
	return firstNonEmpty(current, "-") + " -> " + firstNonEmpty(proposed, "-")
}

// formatPlanInterval writes an interval the way specs do, such as 6h or 1h30m.
func formatPlanInterval(seconds *int) string {
	if seconds == nil {
		return "-"
	}
	value := (time.Duration(*seconds) * time.Second).String()
	if strings.HasSuffix(value, "m0s") {
		value = strings.TrimSuffix(value, "0s")
	}
	if strings.HasSuffix(value, "h0m") {
		value = strings.TrimSuffix(value, "0m")
	}
	return value
}

func formatPlanSkillLocks(skills []planSkillLock) string {
	if len(skills) == 0 {
		return "-"
	}
	values := make([]string, 0, len(skills))
	for _, skill := range skills {
		value := skill.Name
		if skill.Ref != "" {
			value += " " + skill.Ref
		}
		value += " " + skill.Digest
		if skill.Starred {
			value += " *"
		}
		values = append(values, value)
	}
	return strings.Join(values, ", ")
}

func skillNames(skills []*spec.Skill) []string {
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}

// skillDisplayNames marks required rubrics with the same trailing star used in
// spec frontmatter.
func skillDisplayNames(compiled *spec.CompiledEnvironment) []string {
	required := map[string]bool{}
	for _, s := range compiled.RequiredVerifierSkills {
		required[s.Name] = true
	}
	var names []string
	for _, s := range compiled.Skills {
		name := s.Name
		if required[name] {
			name += "*"
		}
		names = append(names, name)
	}
	return names
}
