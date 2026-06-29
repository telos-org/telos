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
	record, err := pushSpecPackage(client, pkg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(map[string]any{
			"name":    pkg.name,
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
		name:   compiled.Environment.Name,
		digest: pkg.Digest,
		bytes:  pkg.Bytes,
	}, nil
}

func pushSpecPackage(client *cloud.Client, pkg *specPackage) (*cloud.ApplyPackageRecord, error) {
	if _, err := client.UploadApplyPackage(pkg.digest, pkg.bytes); err != nil {
		return nil, err
	}
	return client.UpdateApplyPackageMetadata(pkg.digest, cloud.ApplyPackageMetadata{
		Name:       pkg.name,
		Visibility: "private",
	})
}

func printPushReceipt(name string, record *cloud.ApplyPackageRecord) {
	fmt.Fprintf(os.Stdout, "pushed %s\n\n", name)
	printSummaryField(os.Stdout, "Digest", record.Digest)
	printSummaryField(os.Stdout, "Size", fmt.Sprintf("%d bytes", record.SizeBytes))
}
