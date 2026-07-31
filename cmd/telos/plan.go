package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
}

func cmdPlan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	sessionID := fs.String("session", "", "Managed session ID to compare")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos plan SPEC.md [--session SESSION] [--json]")
		os.Exit(1)
	}
	specPath := resolveSpecPath(fs.Arg(0))
	if err := prepareRegistrySkills(specPath); err != nil {
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

	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	var comparison *specComparison
	if strings.TrimSpace(*sessionID) != "" {
		comparison, err = compareSessionSpec(*sessionID, proposedSpec, platform)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	targetMode := "local"
	willCreateSession := comparison == nil
	sessionLineage := "root"
	userScope := map[string]interface{}{
		"status": "local",
		"label":  "local workspace",
		"detail": "no cloud auth required",
	}
	if platform != "local" {
		targetMode = "cloud"
		userScope = map[string]interface{}{
			"status": "missing",
			"label":  "not logged in",
			"detail": "run `telos login` before `telos apply`",
		}
		if config.IsConfigured() {
			userScope = map[string]interface{}{
				"status": "configured",
				"label":  "cloud control plane",
				"detail": "stored cloud credentials",
			}
		}
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
		},
		"session": map[string]interface{}{
			"lineage":          sessionLineage,
			"interval_seconds": compiled.Environment.IntervalSeconds,
		},
		"target": map[string]interface{}{
			"mode":                targetMode,
			"will_create_session": willCreateSession,
			"will_update_session": comparison != nil,
			"will_mutate":         false,
		},
		"user": userScope,
	}
	if comparison != nil {
		plan["change"] = map[string]interface{}{
			"session_id":  comparison.sessionID,
			"current_ref": comparison.currentRef,
			"spec_diff":   comparison.diff,
		}
	}

	if *jsonOut {
		printJSON(plan)
		return
	}

	printPlanPreview(os.Stdout, compiled, specPath, platform, sessionLineage, comparison)
}

func printPlanPreview(
	out io.Writer,
	compiled *spec.CompiledEnvironment,
	specPath string,
	platform string,
	sessionLineage string,
	comparison *specComparison,
) {
	printSummaryField(out, "Spec", compiled.Environment.Name)
	printSummaryField(out, "Target", platform)
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
		printSummaryField(out, "Skills", strings.Join(skillNames(compiled.Skills), ", "))
	}
	if comparison == nil {
		return
	}
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

func compareSessionSpec(sessionID string, proposed []byte, platform string) (*specComparison, error) {
	sessionID = strings.TrimSpace(sessionID)
	switch {
	case isLocalApplyID(sessionID):
		if platform != "local" {
			return nil, fmt.Errorf("%s is local but the proposed spec targets %s", sessionID, platform)
		}
		current, err := store().Spec(sessionID)
		if err != nil {
			return nil, err
		}
		return newSpecComparison(sessionID, "current session", []byte(current.Markdown), proposed), nil
	case isCloudApplyID(sessionID):
		if platform == "local" {
			return nil, fmt.Errorf("%s is cloud but the proposed spec targets local", sessionID)
		}
		control, err := cloud.ControlClient()
		if err != nil {
			return nil, err
		}
		return compareCloudSessionSpec(control, sessionID, proposed)
	default:
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
}

func compareCloudSessionSpec(
	control *cloud.Client,
	sessionID string,
	proposed []byte,
) (*specComparison, error) {
	pkg, err := packageForSession(control, sessionID)
	if err != nil {
		return nil, err
	}
	current, err := verifiedPackageSpec(pkg)
	if err != nil {
		return nil, err
	}
	return newSpecComparison(sessionID, pkg.reference.ref, current, proposed), nil
}

func newSpecComparison(sessionID string, currentRef string, current, proposed []byte) *specComparison {
	return &specComparison{
		sessionID:  sessionID,
		currentRef: currentRef,
		diff: string(internaldiff.Diff(
			"deployed/SPEC.md",
			current,
			"proposed/SPEC.md",
			proposed,
		)),
	}
}

func skillNames(skills []*spec.Skill) []string {
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}
