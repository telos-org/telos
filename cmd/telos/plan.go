package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
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
	Rubrics         []string        `json:"required_rubrics"`
}

type planSkillLock struct {
	Name    string `json:"name"`
	Ref     string `json:"ref,omitempty"`
	Digest  string `json:"digest"`
	Starred bool   `json:"required_rubric,omitempty"`
}

func cmdPlan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	sessionID := fs.String("session", "", "Managed session ID to compare")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos plan SPEC.md [--session SESSION] [--json] [--context CONTEXT]")
		os.Exit(1)
	}
	specPath := resolveSpecPath(fs.Arg(0))
	if err := prepareRegistrySkillsForContext(specPath, contextOverride); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	proposedSpec, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	proposedState, err := planSpecStateForCompiled(compiled, proposedSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: build plan metadata: %v\n", err)
		os.Exit(1)
	}

	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	var comparison *specComparison
	if strings.TrimSpace(*sessionID) != "" {
		comparison, err = compareSessionSpec(
			*sessionID,
			proposedSpec,
			proposedState,
			platform,
			contextOverride,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	targetMode := "local"
	targetContext := ""
	willCreateSession := comparison == nil
	sessionLineage := "root"
	userScope := map[string]interface{}{
		"status": "local",
		"label":  "local workspace",
		"detail": "no cloud auth required",
	}
	if platform != "local" {
		targetMode = "cloud"
		cfg, err := config.LoadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		targetContext = strings.TrimSpace(contextOverride)
		if targetContext == "" {
			targetContext = strings.TrimSpace(cfg.Context)
		}
		if targetContext == "" {
			targetContext = "personal"
		}
		userScope = map[string]interface{}{
			"status":  "missing",
			"label":   "not logged in",
			"detail":  "run `telos login` before `telos apply`",
			"context": targetContext,
		}
		if cfg.AuthToken != "" {
			control, err := cloud.ControlClientForContext(contextOverride)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			targetContext = resolvedCloudContext(control)
			userScope = map[string]interface{}{
				"status":  "configured",
				"label":   "cloud control plane",
				"detail":  "effective cloud credentials",
				"context": targetContext,
			}
		}
	}
	targetScope := map[string]interface{}{
		"mode":                targetMode,
		"will_create_session": willCreateSession,
		"will_update_session": comparison != nil,
		"will_mutate":         false,
	}
	if targetContext != "" {
		targetScope["context"] = targetContext
	}
	plan := map[string]interface{}{
		"spec": map[string]interface{}{
			"name":         compiled.Environment.Name,
			"path":         specPath,
			"content_hash": compiled.ContentHash,
			"platform":     platform,
			"namespace":    compiled.Namespace,
			"lineage":      compiled.Lineage,
			"skills":       skillNames(compiled.Skills),
			"required_verifier_skills": skillNames(
				compiled.RequiredVerifierSkills,
			),
		},
		"session": map[string]interface{}{
			"lineage":          sessionLineage,
			"interval_seconds": compiled.Environment.IntervalSeconds,
		},
		"target": targetScope,
		"user":   userScope,
	}
	if comparison != nil {
		plan["change"] = map[string]interface{}{
			"session_id":  comparison.sessionID,
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

	printPlanPreview(os.Stdout, compiled, specPath, platform, targetContext, sessionLineage, comparison)
}

func printPlanPreview(
	out io.Writer,
	compiled *spec.CompiledEnvironment,
	specPath string,
	platform string,
	contextName string,
	sessionLineage string,
	comparison *specComparison,
) {
	printSummaryField(out, "Spec", compiled.Environment.Name)
	printSummaryField(out, "Target", platform)
	if contextName != "" {
		printSummaryField(out, "Context", contextName)
	}
	printSummaryField(out, "Lineage", sessionLineage)
	if comparison != nil {
		printSummaryField(out, "Session", comparison.sessionID)
		printSummaryField(out, "Current", comparison.currentRef)
	}
	printSummaryField(out, "Mutates", "no")
	printSummaryField(out, "Path", specPath)
	if platform != "local" {
		printSummaryField(out, "Namespace", compiled.Namespace)
	}
	printSummaryField(out, "Hash", compiled.ContentHash)
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
	platform string,
	contextOverride string,
) (*specComparison, error) {
	sessionID = strings.TrimSpace(sessionID)
	switch {
	case isLocalApplyID(sessionID):
		if platform != "local" {
			return nil, fmt.Errorf("%s is local but the proposed spec targets %s", sessionID, platform)
		}
		localStore := store()
		current, err := localStore.Spec(sessionID)
		if err != nil {
			return nil, err
		}
		localSession, err := localStore.Get(sessionID)
		if err != nil {
			return nil, err
		}
		if localSession.SessionDir == nil || strings.TrimSpace(*localSession.SessionDir) == "" {
			return nil, fmt.Errorf("local session %s has no session directory", sessionID)
		}
		currentState, err := planLocalSessionSpecState(
			[]byte(current.Markdown),
			*localSession.SessionDir,
		)
		if err != nil {
			return nil, err
		}
		return newSpecComparisonWithStates(
			sessionID,
			"current session",
			[]byte(current.Markdown),
			proposed,
			currentState,
			proposedState,
		), nil
	case isCloudApplyID(sessionID):
		if platform == "local" {
			return nil, fmt.Errorf("%s is cloud but the proposed spec targets local", sessionID)
		}
		control, err := cloud.ControlClientForContext(contextOverride)
		if err != nil {
			return nil, err
		}
		return compareCloudSessionSpecWithState(control, sessionID, proposed, proposedState)
	default:
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
}

func planLocalSessionSpecState(
	markdown []byte,
	sessionDir string,
) (planSpecState, error) {
	manifest, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return planSpecState{}, fmt.Errorf("read current local session metadata: %w", err)
	}
	return planSpecStateFromMarkdown(markdown, manifest.ApplyPackageLock)
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
	state := planSpecState{Skills: []planSkillLock{}, Rubrics: []string{}}
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
		if lock.Starred {
			state.Rubrics = append(state.Rubrics, name)
		}
	}
	return state, nil
}

func printPlanStateDelta(out io.Writer, current, proposed planSpecState) {
	if !hasPlanSpecState(current) && !hasPlanSpecState(proposed) {
		return
	}
	printSummaryField(out, "Version", planDeltaValue(current.Version, proposed.Version))
	printSummaryField(
		out,
		"Interval",
		planDeltaValue(formatPlanInterval(current.IntervalSeconds), formatPlanInterval(proposed.IntervalSeconds)),
	)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Skill locks")
	printDetailField(out, "current", formatPlanSkillLocks(current.Skills))
	printDetailField(out, "proposed", formatPlanSkillLocks(proposed.Skills))
	printDetailField(out, "rubrics", planDeltaValue(formatPlanList(current.Rubrics), formatPlanList(proposed.Rubrics)))
}

func hasPlanSpecState(state planSpecState) bool {
	return state.Version != "" || state.IntervalSeconds != nil || len(state.Skills) > 0 || len(state.Rubrics) > 0
}

func planDeltaValue(current, proposed string) string {
	return firstNonEmpty(current, "-") + " -> " + firstNonEmpty(proposed, "-")
}

func formatPlanInterval(seconds *int) string {
	if seconds == nil {
		return "-"
	}
	return (time.Duration(*seconds) * time.Second).String()
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

func formatPlanList(values []string) string {
	if len(values) == 0 {
		return "-"
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

// skillDisplayNames marks required verifier skills with the same trailing
// star the spec frontmatter uses to declare them.
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
