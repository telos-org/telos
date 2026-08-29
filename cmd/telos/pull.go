package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

var packageRefSegmentRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

type packageReference struct {
	scope   string
	name    string
	version string
	ref     string
}

type registryReference struct {
	scope   string
	name    string
	version string
}

type pulledPackage struct {
	reference packageReference
	digest    string
	data      []byte
}

func cmdGet(args []string) {
	fs := newCommandFlagSet("get", "telos get SESSION [flags]")
	output := fs.String("output", "", "Destination package directory or Markdown file")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	requireArgCount(fs, 1, "one SESSION")
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		exitWithError(err)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		exitWithError(err)
	}
	pkg, err := packageForSession(control, fs.Arg(0))
	if err != nil {
		exitWithError(err)
	}
	path, err := materializePackage(control, pkg, *output)
	if err != nil {
		exitWithError(err)
	}
	printPackageReceipt("got", pkg, path)
}

func cmdPull(args []string) {
	fs, output, contextValue := newPullFlagSet()
	parseFlags(fs, args)
	if fs.NArg() > 0 && strings.EqualFold(strings.TrimSpace(fs.Arg(0)), "skill") {
		requireArgCount(fs, 2, "skill and an exact @scope/name:version")
		reference, err := parseRegistryReference(fs.Arg(1))
		if err != nil {
			exitWithError(err)
		}
		if reference.version == "" {
			fmt.Fprintln(os.Stderr, "error: skill pull requires an exact version")
			os.Exit(2)
		}
		client := registryReadClient(fs, *contextValue)
		destination, record, err := pullRegistrySkill(client, reference, *output)
		if err != nil {
			exitWithError(err)
		}
		fmt.Printf("pulled %s (%s) to %s\n", record.Ref, record.Digest, destination)
		return
	}

	requireArgCount(fs, 1, "one PACKAGE or skill and an exact @scope/name:version")
	reference, err := parsePackageReference(fs.Arg(0))
	if err != nil {
		exitWithError(err)
	}
	control := registryReadClient(fs, *contextValue)
	pkg, err := packageForReference(control, reference)
	if err != nil {
		exitWithError(err)
	}
	path, err := materializePackage(control, pkg, *output)
	if err != nil {
		exitWithError(err)
	}
	printPackageReceipt("pulled", pkg, path)
}

func newPullFlagSet() (*flag.FlagSet, *string, *string) {
	fs := newCommandFlagSet(
		"pull",
		"telos pull @scope/name:version [flags]\n"+
			"       telos pull skill @scope/name:version [flags]",
	)
	output := fs.String("output", "", "Destination package or skill path")
	contextValue := cloudContextFlag(fs)
	return fs, output, contextValue
}

func registryReadClient(fs *flag.FlagSet, contextValue string) *cloud.Client {
	contextOverride, err := cloudContextOverride(fs, contextValue)
	if err != nil {
		exitWithError(err)
	}
	client, err := cloud.RegistryReadClientForContext(contextOverride)
	if err != nil {
		exitWithError(err)
	}
	if client.Token == "" {
		if err := requireRegistryPrivacyCapability(client); err != nil {
			exitWithError(err)
		}
	}
	return client
}

func parseRegistryReference(raw string) (registryReference, error) {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(value, "@") {
		return registryReference{}, fmt.Errorf("registry reference must start with @scope/name")
	}
	scope, rest, ok := strings.Cut(strings.TrimPrefix(value, "@"), "/")
	if !ok {
		return registryReference{}, fmt.Errorf("invalid registry reference %q", value)
	}
	name, version, ok := strings.Cut(rest, ":")
	if !ok || strings.Contains(version, ":") || !packageSemverRE.MatchString(version) {
		return registryReference{}, fmt.Errorf("skill reference requires an exact semantic version")
	}
	if !packageRefSegmentRE.MatchString(scope) || !packageRefSegmentRE.MatchString(name) {
		return registryReference{}, fmt.Errorf("invalid registry reference %q", value)
	}
	canonical := "@" + scope + "/" + name + ":" + version
	if canonical != value {
		return registryReference{}, fmt.Errorf("registry reference must be canonical: %s", canonical)
	}
	return registryReference{scope: scope, name: name, version: version}, nil
}

func pullRegistrySkill(
	client *cloud.Client,
	reference registryReference,
	output string,
) (string, *cloud.SkillRecord, error) {
	if client == nil {
		return "", nil, fmt.Errorf("Registry client is required")
	}
	if reference.version == "" {
		return "", nil, fmt.Errorf("skill pull requires an exact Registry version")
	}
	record, err := client.GetSkillVersion(
		reference.scope,
		reference.name,
		reference.version,
	)
	if err != nil {
		return "", nil, fmt.Errorf(
			"resolve @%s/%s:%s: %w",
			reference.scope,
			reference.name,
			reference.version,
			err,
		)
	}
	if record.Scope != reference.scope ||
		record.Name != reference.name ||
		record.Version != reference.version {
		return "", nil, fmt.Errorf("Registry returned mismatched skill %s", record.Ref)
	}
	bundle, err := client.DownloadSkillVersionBundle(
		reference.scope,
		reference.name,
		reference.version,
	)
	if err != nil {
		return "", nil, fmt.Errorf("download %s: %w", record.Ref, err)
	}
	if err := spec.VerifySkillBundle(reference.name, record.Digest, bundle); err != nil {
		return "", nil, fmt.Errorf("verify %s: %w", record.Ref, err)
	}
	destination := strings.TrimSpace(output)
	if destination == "" {
		destination = reference.name
	}
	destination = filepath.Clean(destination)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", nil, err
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("%s already exists", destination)
		}
		return "", nil, err
	}
	// The successful Mkdir is the ownership boundary: only this invocation's
	// directory may be removed if extraction fails. A concurrent creator can
	// no longer be overwritten or deleted after a check-then-act gap.
	if err := spec.ExtractSkillBundle(
		reference.name,
		record.Digest,
		bundle,
		destination,
	); err != nil {
		_ = os.RemoveAll(destination)
		return "", nil, fmt.Errorf("extract %s: %w", record.Ref, err)
	}
	return destination, record, nil
}

func packageForSession(control *cloud.Client, sessionID string) (*pulledPackage, error) {
	session, err := control.GetSession(strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	reference, err := parsePackageReference(session.PackageRef)
	if err != nil {
		return nil, fmt.Errorf("session %s has invalid package_ref: %w", session.ID, err)
	}
	data, err := control.DownloadPackageVersionBundle(
		reference.scope,
		reference.name,
		reference.version,
	)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", reference.ref, err)
	}
	return &pulledPackage{
		reference: reference,
		digest:    strings.TrimSpace(session.PackageDigest),
		data:      data,
	}, nil
}

func packageForReference(control *cloud.Client, reference packageReference) (*pulledPackage, error) {
	pkg, _, err := resolvePackageReference(control, reference)
	return pkg, err
}

func resolvePackageReference(
	control *cloud.Client,
	reference packageReference,
) (*pulledPackage, *cloud.PackageVersionRecord, error) {
	record, err := control.GetPackageVersion(reference.scope, reference.name, reference.version)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve %s: %w", reference.ref, err)
	}
	if record.Scope != reference.scope ||
		record.Name != reference.name ||
		record.Version != reference.version ||
		record.Ref != reference.ref {
		return nil, nil, fmt.Errorf("resolve %s: registry returned %s", reference.ref, record.Ref)
	}
	data, err := control.DownloadPackageVersionBundle(
		reference.scope,
		reference.name,
		reference.version,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("download %s: %w", reference.ref, err)
	}
	return &pulledPackage{
		reference: reference,
		digest:    strings.TrimSpace(record.Digest),
		data:      data,
	}, record, nil
}

func registryPackageForApply(
	control *cloud.Client,
	reference packageReference,
) (*cloud.PackageVersionRecord, error) {
	pkg, record, err := resolvePackageReference(control, reference)
	if err != nil {
		return nil, err
	}
	if _, err := verifiedPackageSpec(pkg); err != nil {
		return nil, err
	}
	return record, nil
}

func materializePackage(control *cloud.Client, pkg *pulledPackage, output string) (string, error) {
	if pkg == nil {
		return "", fmt.Errorf("package is required")
	}
	rootSpec, err := verifiedPackageSpec(pkg)
	if err != nil {
		return "", err
	}

	destination := strings.TrimSpace(output)
	if destination == "" {
		destination = pkg.reference.name
	}
	if strings.EqualFold(filepath.Ext(destination), ".md") {
		return destination, writePackageSpec(rootSpec, destination)
	}
	hydrated, _, err := spec.HydrateApplyPackage(pkg.data, registrySkillFetcher(control))
	if err != nil {
		return "", fmt.Errorf("hydrate %s: %w", pkg.reference.ref, err)
	}
	return destination, extractPackageDirectory(hydrated, destination)
}

func verifiedPackageSpec(pkg *pulledPackage) ([]byte, error) {
	rootSpec, _, err := verifiedPackageContents(pkg)
	return rootSpec, err
}

func verifiedPackageContents(
	pkg *pulledPackage,
) ([]byte, *spec.ApplyPackageManifest, error) {
	if pkg == nil {
		return nil, nil, fmt.Errorf("package is required")
	}
	rootSpec, manifest, err := spec.ApplyPackageSpec(pkg.data)
	if err != nil {
		return nil, nil, fmt.Errorf("verify %s: %w", pkg.reference.ref, err)
	}
	actualDigest := spec.ApplyPackageDigest(manifest)
	if pkg.digest == "" {
		return nil, nil, fmt.Errorf("%s has no package digest", pkg.reference.ref)
	}
	if actualDigest != pkg.digest {
		return nil, nil, fmt.Errorf(
			"%s digest mismatch: got %s want %s",
			pkg.reference.ref,
			actualDigest,
			pkg.digest,
		)
	}
	return rootSpec, manifest, nil
}

func registrySkillFetcher(control *cloud.Client) spec.ApplyPackageSkillFetcher {
	return func(request spec.ApplyPackageSkillFetchRequest) ([]byte, error) {
		reference, ok := spec.ParseRegistrySkillRef(request.Ref)
		if !ok || reference.Version == "" {
			return nil, fmt.Errorf("invalid versioned skill ref %q", request.Ref)
		}
		record, err := control.GetSkillVersion(reference.Scope, reference.Name, reference.Version)
		if err != nil {
			return nil, err
		}
		if record.Digest != request.Digest {
			return nil, fmt.Errorf(
				"skill %s digest mismatch: got %s want %s",
				reference.Ref,
				record.Digest,
				request.Digest,
			)
		}
		return control.DownloadSkillVersionBundle(
			reference.Scope,
			reference.Name,
			reference.Version,
		)
	}
}

func writePackageSpec(markdown []byte, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists", destination)
		}
		return err
	}
	if _, err := file.Write(markdown); err != nil {
		file.Close()
		os.Remove(destination)
		return err
	}
	return file.Close()
}

func extractPackageDirectory(data []byte, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("%s already exists", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(destination)+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if _, err := spec.ExtractApplyPackage(data, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, destination)
}

func parsePackageReference(raw string) (packageReference, error) {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(value, "@") {
		return packageReference{}, fmt.Errorf("package must be an exact @scope/name:version reference")
	}
	scope, rest, ok := strings.Cut(strings.TrimPrefix(value, "@"), "/")
	if !ok {
		return packageReference{}, fmt.Errorf("invalid package reference %q", value)
	}
	name, version, ok := strings.Cut(rest, ":")
	if !ok || strings.Contains(version, ":") ||
		!packageRefSegmentRE.MatchString(scope) ||
		!packageRefSegmentRE.MatchString(name) ||
		!packageSemverRE.MatchString(version) {
		return packageReference{}, fmt.Errorf("invalid package reference %q", value)
	}
	return packageReference{
		scope:   scope,
		name:    name,
		version: version,
		ref:     value,
	}, nil
}

func printPackageReceipt(operation string, pkg *pulledPackage, path string) {
	fmt.Printf("%s %s\n\n", operation, pkg.reference.ref)
	printSummaryField(os.Stdout, "Package", pkg.reference.ref)
	printSummaryField(os.Stdout, "Digest", pkg.digest)
	printSummaryField(os.Stdout, "Path", path)
}

func exitWithError(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
