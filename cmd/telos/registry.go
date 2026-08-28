package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
)

type registryReference struct {
	scope   string
	name    string
	version string
	ref     string
}

func cmdRegistry(args []string) {
	if len(args) == 0 || isHelpArg(args[0]) {
		registryUsage(os.Stdout)
		return
	}
	switch args[0] {
	case "list":
		cmdRegistryList(args[1:])
	case "inspect":
		cmdRegistryInspect(args[1:])
	case "visibility":
		cmdRegistryVisibility(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown registry command: %s\n\n", args[0])
		registryUsage(os.Stderr)
		os.Exit(2)
	}
}

func registryUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: telos registry <command> [args]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "commands:")
	fmt.Fprintln(out, "  list packages|skills                         Discover accessible identities")
	fmt.Fprintln(out, "  inspect package|skill @scope/name[:version] Inspect an identity or version")
	fmt.Fprintln(out, "  visibility package|skill @scope/name MODE   Change identity-wide visibility")
}

func cmdRegistryList(args []string) {
	fs := newCommandFlagSet(
		"registry list",
		"telos registry list packages|skills [flags]",
	)
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	requireArgCount(fs, 1, "packages or skills")
	client := registryReadClient(fs, *contextValue)

	switch strings.ToLower(strings.TrimSpace(fs.Arg(0))) {
	case "package", "packages":
		records, err := client.ListPackages()
		if err != nil {
			exitWithError(err)
		}
		if *jsonOut {
			printJSON(map[string]any{"packages": records})
			return
		}
		printRegistryPackages(records)
	case "skill", "skills":
		records, err := client.ListSkills()
		if err != nil {
			exitWithError(err)
		}
		if *jsonOut {
			printJSON(map[string]any{"skills": records})
			return
		}
		printRegistrySkills(records)
	default:
		fmt.Fprintln(os.Stderr, "error: registry kind must be packages or skills")
		os.Exit(2)
	}
}

func cmdRegistryInspect(args []string) {
	fs := newCommandFlagSet(
		"registry inspect",
		"telos registry inspect package|skill @scope/name[:version] [flags]",
	)
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	requireArgCount(fs, 2, "a registry kind and @scope/name[:version]")
	reference, err := parseRegistryReference(fs.Arg(1))
	if err != nil {
		exitWithError(err)
	}
	client := registryReadClient(fs, *contextValue)

	switch strings.ToLower(strings.TrimSpace(fs.Arg(0))) {
	case "package", "packages":
		if reference.version == "" {
			record, err := client.GetPackage(reference.scope, reference.name)
			if err != nil {
				exitWithError(err)
			}
			if *jsonOut {
				printJSON(record)
				return
			}
			printRegistryPackage(record)
			return
		}
		record, err := client.GetPackageVersion(
			reference.scope,
			reference.name,
			reference.version,
		)
		if err != nil {
			exitWithError(err)
		}
		if *jsonOut {
			printJSON(record)
			return
		}
		printSummaryField(os.Stdout, "Ref", record.Ref)
		printSummaryField(os.Stdout, "Digest", record.Digest)
		printSummaryField(os.Stdout, "Created", record.CreatedAt)
	case "skill", "skills":
		var record *cloud.SkillRecord
		if reference.version == "" {
			record, err = client.GetSkill(reference.scope, reference.name)
		} else {
			record, err = client.GetSkillVersion(
				reference.scope,
				reference.name,
				reference.version,
			)
		}
		if err != nil {
			exitWithError(err)
		}
		if *jsonOut {
			printJSON(record)
			return
		}
		printRegistrySkill(record)
	default:
		fmt.Fprintln(os.Stderr, "error: registry kind must be package or skill")
		os.Exit(2)
	}
}

func cmdRegistryVisibility(args []string) {
	fs := newCommandFlagSet(
		"registry visibility",
		"telos registry visibility package|skill @scope/name public|private --confirm @scope/name [flags]",
	)
	confirmation := fs.String(
		"confirm",
		"",
		"Confirm the identity-wide change by repeating @scope/name",
	)
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	requireArgCount(fs, 3, "a registry kind, @scope/name, and public or private")
	reference, err := parseRegistryReference(fs.Arg(1))
	if err != nil {
		exitWithError(err)
	}
	if reference.version != "" {
		fmt.Fprintln(os.Stderr, "error: visibility belongs to @scope/name, not an individual version")
		os.Exit(2)
	}
	target := strings.ToLower(strings.TrimSpace(fs.Arg(2)))
	if target != "public" && target != "private" {
		fmt.Fprintln(os.Stderr, "error: visibility must be public or private")
		os.Exit(2)
	}
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		exitWithError(err)
	}
	client, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		exitWithError(err)
	}
	if err := requireRegistryPrivacyCapability(client); err != nil {
		exitWithError(err)
	}

	kind := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	var preflight *cloud.RegistryVisibilityPreflight
	switch kind {
	case "package", "packages":
		preflight, err = client.PreflightPackageVisibility(
			reference.scope,
			reference.name,
			target,
		)
	case "skill", "skills":
		preflight, err = client.PreflightSkillVisibility(
			reference.scope,
			reference.name,
			target,
		)
	default:
		fmt.Fprintln(os.Stderr, "error: registry kind must be package or skill")
		os.Exit(2)
	}
	if err != nil {
		exitWithError(err)
	}
	printVisibilityPreflight(preflight)
	if len(preflight.Blockers) > 0 {
		fmt.Fprintln(os.Stderr, "error: visibility change is blocked")
		os.Exit(1)
	}
	if strings.TrimSpace(*confirmation) != reference.ref {
		fmt.Fprintf(
			os.Stderr,
			"error: review the warning, then repeat with --confirm %s\n",
			reference.ref,
		)
		os.Exit(2)
	}

	switch kind {
	case "package", "packages":
		record, err := client.ChangePackageVisibility(
			reference.scope,
			reference.name,
			target,
			preflight.ConfirmationToken,
		)
		if err != nil {
			exitWithError(err)
		}
		fmt.Printf("visibility changed: %s is now %s\n", record.Ref, record.Visibility)
	case "skill", "skills":
		record, err := client.ChangeSkillVisibility(
			reference.scope,
			reference.name,
			target,
			preflight.ConfirmationToken,
		)
		if err != nil {
			exitWithError(err)
		}
		fmt.Printf("visibility changed: @%s/%s is now %s\n", record.Scope, record.Name, record.Visibility)
	}
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

func requireRegistryPrivacyCapability(client *cloud.Client) error {
	capabilities, err := client.RegistryCapabilities()
	if err != nil {
		return fmt.Errorf("check Registry rollout: %w", err)
	}
	if !capabilities.RegistryPrivacy {
		return fmt.Errorf("Registry public access and visibility controls are not enabled on this control plane")
	}
	return nil
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
	name := rest
	version := ""
	if strings.Contains(rest, ":") {
		var found bool
		name, version, found = strings.Cut(rest, ":")
		if !found || strings.Contains(version, ":") || !packageSemverRE.MatchString(version) {
			return registryReference{}, fmt.Errorf("invalid registry reference %q", value)
		}
	}
	if !packageRefSegmentRE.MatchString(scope) || !packageRefSegmentRE.MatchString(name) {
		return registryReference{}, fmt.Errorf("invalid registry reference %q", value)
	}
	canonical := "@" + scope + "/" + name
	if version != "" {
		canonical += ":" + version
	}
	if canonical != value {
		return registryReference{}, fmt.Errorf("registry reference must be canonical: %s", canonical)
	}
	return registryReference{
		scope:   scope,
		name:    name,
		version: version,
		ref:     "@" + scope + "/" + name,
	}, nil
}

func printRegistryPackages(records []cloud.PackageRecord) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REF\tVISIBILITY\tLATEST\tDESCRIPTION")
	for _, record := range records {
		latest := "-"
		if record.LatestVersion != nil {
			latest = record.LatestVersion.Version
		}
		description := ""
		if record.Description != nil {
			description = strings.TrimSpace(*record.Description)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", record.Ref, record.Visibility, latest, description)
	}
	_ = w.Flush()
}

func printRegistrySkills(records []cloud.SkillRecord) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REF\tVISIBILITY\tDIGEST\tDESCRIPTION")
	for _, record := range records {
		description := ""
		if record.Description != nil {
			description = strings.TrimSpace(*record.Description)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", record.Ref, record.Visibility, record.Digest, description)
	}
	_ = w.Flush()
}

func printRegistryPackage(record *cloud.PackageRecord) {
	printSummaryField(os.Stdout, "Ref", record.Ref)
	printSummaryField(os.Stdout, "Visibility", record.Visibility)
	printSummaryField(os.Stdout, "Versions", fmt.Sprintf("%d", len(record.Versions)))
	if record.LatestVersion != nil {
		printSummaryField(os.Stdout, "Latest", record.LatestVersion.Ref)
		printSummaryField(os.Stdout, "Digest", record.LatestVersion.Digest)
	}
}

func printRegistrySkill(record *cloud.SkillRecord) {
	printSummaryField(os.Stdout, "Ref", record.Ref)
	printSummaryField(os.Stdout, "Visibility", record.Visibility)
	printSummaryField(os.Stdout, "Digest", record.Digest)
	printSummaryField(os.Stdout, "Files", fmt.Sprintf("%d", record.FileCount))
}

func printVisibilityPreflight(preflight *cloud.RegistryVisibilityPreflight) {
	identity := "@" + preflight.Scope + "/" + preflight.Name
	fmt.Printf(
		"%s visibility: %s -> %s\n",
		identity,
		preflight.CurrentVisibility,
		preflight.TargetVisibility,
	)
	fmt.Printf("Affected immutable versions: %d\n", preflight.VersionCount)
	if preflight.Warning != nil && strings.TrimSpace(*preflight.Warning) != "" {
		fmt.Printf("Warning: %s\n", strings.TrimSpace(*preflight.Warning))
	}
	for _, blocker := range preflight.Blockers {
		if blocker.Ref != nil && strings.TrimSpace(*blocker.Ref) != "" {
			fmt.Printf("Blocked by %s: %s\n", strings.TrimSpace(*blocker.Ref), blocker.Message)
		} else {
			fmt.Printf("Blocked: %s\n", blocker.Message)
		}
	}
}
