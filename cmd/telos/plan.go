package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/spec"
)

// -- plan ---------------------------------------------------------------------

func cmdPlan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos plan SPEC.md [--json]")
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

	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	targetMode := "local"
	willCreateSession := platform == "local"
	sessionLineage := "root"
	userScope := map[string]interface{}{
		"status": "local",
		"label":  "local workspace",
		"detail": "no cloud auth required",
	}
	if platform != "local" {
		targetMode = "cloud"
		willCreateSession = true
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
			"will_mutate":         false,
		},
		"user": userScope,
	}

	if *jsonOut {
		printJSON(plan)
		return
	}

	printPlanPreview(os.Stdout, compiled, specPath, platform, sessionLineage)
}

func printPlanPreview(
	out io.Writer,
	compiled *spec.CompiledEnvironment,
	specPath string,
	platform string,
	sessionLineage string,
) {
	printSummaryField(out, "Spec", compiled.Environment.Name)
	printSummaryField(out, "Target", platform)
	printSummaryField(out, "Lineage", sessionLineage)
	printSummaryField(out, "Mutates", "no")
	printSummaryField(out, "Path", specPath)
	if platform != "local" {
		printSummaryField(out, "Namespace", compiled.Namespace)
	}
	printSummaryField(out, "Hash", compiled.ContentHash)
	if len(compiled.Skills) > 0 {
		printSummaryField(out, "Skills", strings.Join(skillNames(compiled.Skills), ", "))
	}
}

func skillNames(skills []*spec.Skill) []string {
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}
