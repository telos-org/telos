package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/spec"
)

// -- plan ---------------------------------------------------------------------

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
	fs := newCommandFlagSet("plan", "telos plan SPEC.md [--out=FILE] [flags]")
	sessionID := fs.String("goal", "", "ID of the Goal to compare against")
	jsonOut := fs.Bool("json", false, "JSON output")
	output := fs.String("out", "", "Save a Cloud Change Request and write its reference to this file")
	message := ""
	fs.StringVar(&message, "message", "", "Describe the change; required with --out")
	fs.StringVar(&message, "m", "", "Shorthand for --message")
	model := fs.String("model", "", "Model for a new Cloud deployment; defaults to $TELOS_MODEL")
	thinking := fs.String("thinking", "", "Thinking effort for a new Cloud deployment; defaults to $TELOS_THINKING")
	force := fs.Bool("force", false, "Save the snapshot bypass for an existing Cloud deployment")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	requireArgCount(fs, 1, "one SPEC.md")
	if !strings.HasPrefix(strings.TrimSpace(fs.Arg(0)), "@") {
		if _, ok := existingSpecPath(fs.Arg(0)); !ok {
			exitWithError(specFileNotFound(fs.Arg(0)))
		}
	}
	*sessionID = strings.TrimSpace(*sessionID)
	if err := validateApplySession(*sessionID); err != nil {
		exitWithError(err)
	}

	if err := validateForceApply(*force, *sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	runtimeConfig, err := resolveSessionRuntimeConfigFromFlags(fs, *model, *thinking, 0)
	if err != nil {
		exitWithError(err)
	}
	if *sessionID != "" && cloudRuntimeConfigSet(runtimeConfig) {
		fmt.Fprintln(os.Stderr, "error: --model and --thinking can only configure a new Cloud deployment")
		os.Exit(2)
	}
	mode := "preview"
	if flagNameSet(fs, "out") {
		mode = "saved"
	}
	message, err = normalizePlanMessage(message, mode == "saved")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if err := runCloudPlan(cloudPlanInput{
		specArg: fs.Arg(0), sessionID: *sessionID, contextOverride: contextOverride,
		runtimeConfig: runtimeConfig, force: *force, mode: mode, revisionMessage: message,
	}, *output, *jsonOut); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func planSpecStateFromMarkdown(markdown []byte) (planSpecState, error) {
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
