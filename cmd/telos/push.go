package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type specPackage struct {
	name    string
	version string
	digest  string
	bytes   []byte
}

var packageVersionRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)

func cmdPush(args []string) {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	scope := fs.String("scope", "", "Package scope")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos push SPEC.md --scope SCOPE [--json]")
		os.Exit(1)
	}

	pkg, err := packageSpec(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	client, err := cloud.ControlClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	record, err := pushSpecPackage(client, pkg, *scope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(map[string]any{
			"name":    pkg.name,
			"version": pkg.version,
			"package": record,
		})
		return
	}
	printPushReceipt(pkg.name, record)
}

func packageSpec(input string) (*specPackage, error) {
	path, ok := existingSpecPath(input)
	if !ok {
		if input == "" {
			return nil, fmt.Errorf("empty spec")
		}
		return nil, fmt.Errorf("spec file not found: %s", input)
	}
	compiled, err := spec.CompileEnvironment(path)
	if err != nil {
		return nil, err
	}
	pkg, err := spec.BuildApplyPackage(compiled, spec.ApplyPackageOptions{CompilerVersion: Version})
	if err != nil {
		return nil, err
	}
	return &specPackage{
		name:    compiled.Environment.Name,
		version: compiled.Environment.PackageVersion,
		digest:  pkg.Digest,
		bytes:   pkg.Bytes,
	}, nil
}

func pushSpecPackage(client *cloud.Client, pkg *specPackage, scope string) (*cloud.PackageVersionRecord, error) {
	if pkg == nil {
		return nil, fmt.Errorf("package is required")
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return nil, fmt.Errorf("--scope is required for package publishing")
	}
	version, err := normalizePackageVersion(pkg.version)
	if err != nil {
		return nil, err
	}
	pkg.version = version
	return client.PublishPackageVersion(scope, pkg.name, version, pkg.bytes)
}

func normalizePackageVersion(raw string) (string, error) {
	version := strings.TrimSpace(raw)
	if version == "" {
		return "", fmt.Errorf("package version is required; set `schema: v0` and `version: 1.0.0` in SPEC.md frontmatter")
	}
	if strings.HasPrefix(version, "v") {
		return "", fmt.Errorf("package version must not start with v: %s", version)
	}
	if !packageVersionRE.MatchString(version) {
		return "", fmt.Errorf("package version must be x, x.y, or x.y.z: %s", version)
	}
	parts := strings.Split(version, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts, "."), nil
}

func printPushReceipt(name string, record *cloud.PackageVersionRecord) {
	fmt.Fprintf(os.Stdout, "pushed %s\n\n", name)
	printSummaryField(os.Stdout, "Ref", record.Ref)
	printSummaryField(os.Stdout, "Digest", record.Digest)
	printSummaryField(os.Stdout, "Version", record.Version)
}
