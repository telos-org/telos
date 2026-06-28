package main

import (
	"flag"
	"fmt"
	"os"

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
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos push SPEC.md [--json]")
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
	response, err := pushSpecPackage(client, pkg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(map[string]any{
			"operation": response.Operation,
			"spec":      response.Spec,
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

func pushSpecPackage(client *cloud.Client, pkg *specPackage) (*cloud.CatalogSpecPushResponse, error) {
	if _, err := client.UploadApplyPackage(pkg.digest, pkg.bytes); err != nil {
		return nil, err
	}
	return client.PushCatalogSpec(pkg.name, pkg.digest)
}

func printPushReceipt(response *cloud.CatalogSpecPushResponse) {
	fmt.Fprintf(os.Stdout, "%s %s\n\n", response.Operation, response.Spec.Name)
	printSummaryField(os.Stdout, "Digest", response.Spec.PackageDigest)
	printSummaryField(os.Stdout, "Ref", response.Spec.Name+"@"+response.Spec.PackageDigest)
}
