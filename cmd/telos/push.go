package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type specPackage struct {
	name   string
	digest string
	bytes  []byte
}

func cmdPush(args []string) {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	scope := fs.String("scope", "default", "Package scope")
	version := fs.String("version", "", "Package version")
	orgID := fs.String("org", "", "Organization ID")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos push SPEC.md [--scope SCOPE] [--version VERSION] [--org ORG] [--json]")
		os.Exit(1)
	}

	pkg, err := packageSpec(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	client, err := cloud.NewControlClientFromConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	applyOrgOverride(client, *orgID)
	response, err := pushSpecPackageVersion(client, pkg, *scope, *version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(map[string]any{
			"package": response,
		})
		return
	}
	printPushReceipt(response)
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
		name:   pkg.Lock.Spec.Name,
		digest: pkg.Digest,
		bytes:  pkg.Bytes,
	}, nil
}

func pushSpecPackage(client *cloud.ControlClient, pkg *specPackage) (*cloud.RegistryVersionRecord, error) {
	return pushSpecPackageVersion(client, pkg, "default", "")
}

func pushSpecPackageVersion(client *cloud.ControlClient, pkg *specPackage, scope string, version string) (*cloud.RegistryVersionRecord, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "default"
	}
	version = strings.TrimSpace(version)
	if version == "" {
		version = versionForDigest(pkg.digest)
	}
	return client.PublishRegistryVersion(scope, pkg.name, version, pkg.bytes)
}

func versionForDigest(digest string) string {
	hex := strings.TrimPrefix(strings.TrimSpace(digest), "sha256:")
	if len(hex) > 12 {
		hex = hex[:12]
	}
	if hex == "" {
		hex = "unknown"
	}
	return "0.0.0-sha." + hex
}

func printPushReceipt(response *cloud.RegistryVersionRecord) {
	fmt.Fprintf(os.Stdout, "published %s\n\n", response.Ref)
	printSummaryField(os.Stdout, "Digest", response.Digest)
	printSummaryField(os.Stdout, "Ref", response.Ref)
}
