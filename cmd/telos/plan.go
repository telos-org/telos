package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/spec"
)

// -- plan ---------------------------------------------------------------------

func cmdPlan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	env := fs.String("env", "", "Existing hosted environment ID")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos plan SPEC.md [--json]")
		os.Exit(1)
	}
	specPath := resolveSpecPath(fs.Arg(0))
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	platform := compiled.Environment.Platform
	if platform == "" {
		platform = "cloud"
	}
	if *env != "" && platform == "local" {
		fmt.Fprintln(os.Stderr, "error: --env cannot be used with platform: local specs")
		os.Exit(1)
	}
	targetMode := "local"
	willAllocateEnvironment := false
	sessionKind := "task"
	userScope := map[string]interface{}{
		"status": "local",
		"label":  "local workspace",
		"detail": "no hosted auth required",
	}
	if platform != "local" {
		sessionKind = "controller"
		if *env != "" {
			targetMode = "hosted env " + *env
		} else {
			targetMode = "hosted"
			willAllocateEnvironment = true
		}
		userScope = map[string]interface{}{
			"status": "missing",
			"label":  "not logged in",
			"detail": "run `telos login` before `telos run`",
		}
		if config.IsConfigured() {
			userScope = map[string]interface{}{
				"status": "configured",
				"label":  "hosted control plane",
				"detail": "stored hosted credentials",
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
			"kind":             sessionKind,
			"interval_seconds": compiled.Environment.IntervalSeconds,
		},
		"target": map[string]interface{}{
			"mode":                      targetMode,
			"will_allocate_environment": willAllocateEnvironment,
			"will_create_session":       true,
			"will_mutate":               false,
		},
		"user": userScope,
	}

	if *jsonOut {
		printJSON(plan)
		return
	}

	fmt.Printf("Plan for %s\n\n", compiled.Environment.Name)
	fmt.Printf("Spec: %s\n", specPath)
	fmt.Printf("Platform: %s\n", platform)
	if platform != "local" {
		fmt.Printf("Namespace: %s\n", compiled.Namespace)
	}
	fmt.Printf("Content hash: %s\n", compiled.ContentHash)
	if len(compiled.Skills) > 0 {
		fmt.Printf("Skills: %s\n", strings.Join(skillNames(compiled.Skills), ", "))
	}
	fmt.Println("\nNo sessions or environments will be created.")
}

func skillNames(skills []*spec.Skill) []string {
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}
