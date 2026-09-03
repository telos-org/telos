package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type specPackage struct {
	name     string
	version  string
	digest   string
	bytes    []byte
	compiled *spec.CompiledEnvironment
}

type skillPackage struct {
	name    string
	version string
	files   map[string]cloud.SkillFile
}

var packageSemverRE = regexp.MustCompile(
	`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
		`(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`,
)
var packageVersionNumberRE = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func cmdPush(args []string) {
	fs := newCommandFlagSet("push", "telos push SPEC.md|SKILL_DIR [flags]")
	scope := fs.String("scope", "", "Package scope")
	version := fs.String("version", "", "Version override for skill or package publishing")
	public := fs.Bool(
		"public",
		false,
		"Create the new identity (and a package's new local skills) as public",
	)
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	requireArgCount(fs, 1, "one SPEC.md or SKILL_DIR")
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	input := fs.Arg(0)
	if skill, ok, err := packageSkillDir(input, *version); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	} else if ok {
		client, err := cloud.ControlClientForContext(contextOverride)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *public {
			if err := requireRegistryPrivacyCapability(client); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
		}
		visibility := ""
		if *public {
			visibility = "public"
		}
		record, err := pushSkillPackageWithVisibility(client, skill, *scope, visibility)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(map[string]any{
				"context": client.ContextName(),
				"name":    skill.name,
				"skill":   record,
			})
			return
		}
		printSkillPushReceipt(skill.name, record)
		return
	}

	pkg, err := packageSpec(input, contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(*version) != "" {
		pkg.version = *version
	}
	client, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *public {
		if err := requireRegistryPrivacyCapability(client); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	visibility := ""
	if *public {
		visibility = "public"
	}
	record, err := pushSpecPackageWithVisibility(
		client,
		pkg,
		*scope,
		visibility,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(map[string]any{
			"context": client.ContextName(),
			"name":    pkg.name,
			"version": pkg.version,
			"package": record,
		})
		return
	}
	printPushReceipt(pkg.name, record)
}

func requireRegistryPrivacyCapability(client *cloud.Client) error {
	capabilities, err := client.RegistryCapabilities()
	if err != nil {
		return fmt.Errorf("check Registry rollout: %w", err)
	}
	if !capabilities.RegistryPrivacy {
		return fmt.Errorf("Registry public access is not enabled on this control plane")
	}
	return nil
}

func packageSpec(input, contextOverride string) (*specPackage, error) {
	path, ok := existingSpecPath(input)
	if !ok {
		if input == "" {
			return nil, fmt.Errorf("empty spec")
		}
		return nil, fmt.Errorf("spec file not found: %s", input)
	}
	if err := prepareRegistrySkillsForContext(path, contextOverride); err != nil {
		return nil, err
	}
	compiled, err := spec.CompileEnvironment(path)
	if err != nil {
		return nil, err
	}
	pkg, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		return nil, err
	}
	return &specPackage{
		name:     compiled.Environment.Name,
		version:  compiled.Environment.Version,
		digest:   pkg.Digest,
		bytes:    pkg.Bytes,
		compiled: compiled,
	}, nil
}

func packageSkillDir(input string, versionOverride string) (*skillPackage, bool, error) {
	dir, ok := existingSkillDir(input)
	if !ok {
		return nil, false, nil
	}
	skillPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, true, err
	}
	raw, body, ok := spec.ParseFrontmatter(string(data))
	if !ok {
		return nil, true, fmt.Errorf("%s has no valid YAML frontmatter", skillPath)
	}
	if strings.TrimSpace(body) == "" {
		return nil, true, fmt.Errorf("%s has empty instructions", skillPath)
	}
	name, ok := raw["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return nil, true, fmt.Errorf("%s frontmatter must set name", skillPath)
	}
	version := strings.TrimSpace(versionOverride)
	if version == "" {
		if rawVersion, ok := raw["version"].(string); ok {
			version = strings.TrimSpace(rawVersion)
		}
	}
	if version != "" {
		version, err = normalizePackageVersion(version)
		if err != nil {
			return nil, true, err
		}
	}
	files, err := readSkillPublishFiles(dir)
	if err != nil {
		return nil, true, err
	}
	return &skillPackage{
		name:    strings.TrimSpace(name),
		version: version,
		files:   files,
	}, true, nil
}

func existingSkillDir(input string) (string, bool) {
	path := strings.TrimSpace(input)
	if path == "" {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if info.Mode().IsRegular() && filepath.Base(path) == "SKILL.md" {
		return filepath.Dir(path), true
	}
	if !info.IsDir() {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
		return path, true
	}
	return "", false
}

func readSkillPublishFiles(root string) (map[string]cloud.SkillFile, error) {
	files := map[string]cloud.SkillFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(rel) == ".telos-managed" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill contains non-regular file: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := "0644"
		if info.Mode().Perm()&0o111 != 0 {
			mode = "0755"
		}
		files[filepath.ToSlash(rel)] = cloud.SkillFile{Mode: mode, Data: data}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := files["SKILL.md"]; !ok {
		return nil, fmt.Errorf("skill missing SKILL.md")
	}
	return files, nil
}

func pushSpecPackage(client *cloud.Client, pkg *specPackage, scope string) (*cloud.PackageVersionRecord, error) {
	return pushSpecPackageWithVisibility(client, pkg, scope, "")
}

func pushSpecPackageVersion(
	client *cloud.Client,
	pkg *specPackage,
	scope string,
	version string,
) (*cloud.PackageVersionRecord, error) {
	return pushSpecPackageVersionWithVisibility(client, pkg, scope, version, "")
}

func pushSpecPackageWithVisibility(
	client *cloud.Client,
	pkg *specPackage,
	scope string,
	visibility string,
) (*cloud.PackageVersionRecord, error) {
	if pkg == nil {
		return nil, fmt.Errorf("package is required")
	}
	return pushSpecPackageVersionWithVisibility(
		client,
		pkg,
		scope,
		pkg.version,
		visibility,
	)
}

func pushSpecPackageVersionWithVisibility(
	client *cloud.Client,
	pkg *specPackage,
	scope string,
	version string,
	visibility string,
) (*cloud.PackageVersionRecord, error) {
	if pkg == nil {
		return nil, fmt.Errorf("package is required")
	}
	scope = strings.TrimSpace(scope)
	version = strings.TrimSpace(version)
	if version != "" {
		normalized, err := normalizePackageVersion(version)
		if err != nil {
			return nil, err
		}
		version = normalized
	}
	skillRefs, err := pushPackageSkills(client, pkg.compiled, scope, visibility)
	if err != nil {
		return nil, err
	}
	rebuilt, err := spec.BuildApplyPackageWithSkillRefs(pkg.compiled, skillRefs)
	if err != nil {
		return nil, err
	}
	pkg.digest = rebuilt.Digest
	pkg.bytes = rebuilt.Bytes
	pkg.version = version
	record, err := client.PublishPackageWithVisibility(
		scope,
		pkg.name,
		version,
		pkg.bytes,
		visibility,
	)
	if err != nil {
		return nil, publicPackagePublishError(err)
	}
	return record, err
}

func publicPackagePublishError(err error) error {
	var apiErr *cloud.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	if apiErr.StatusCode == 400 {
		const privatePrefix = "public package dependency is private: "
		if index := strings.Index(apiErr.Detail, privatePrefix); apiErr.Code == "registry_dependency_private" || index >= 0 {
			ref := strings.TrimSpace(apiErr.Detail)
			if index >= 0 {
				ref = strings.TrimSpace(apiErr.Detail[index+len(privatePrefix):])
			}
			if _, parseErr := parsePackageReference(ref); parseErr == nil {
				return fmt.Errorf(
					"public package requires %s to be public; change its visibility in Telos before retrying",
					ref,
				)
			}
		}
	}
	return registryPublicationError(err)
}

func registryPublicationError(err error) error {
	var apiErr *cloud.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Code {
	case "registry_identity_kind_conflict":
		return fmt.Errorf(
			"Registry identity is already used by another artifact kind; choose a different package or skill name: %w",
			err,
		)
	case "registry_dependency_not_downloadable":
		return fmt.Errorf(
			"public package dependency is not anonymously downloadable: %w",
			err,
		)
	case "registry_dependency_digest_mismatch":
		return fmt.Errorf(
			"package dependency digest does not match its exact Registry version; rebuild the package lock: %w",
			err,
		)
	case "registry_dependency_unavailable":
		return fmt.Errorf(
			"package dependency is missing or inaccessible in this Registry context: %w",
			err,
		)
	}
	if strings.Contains(apiErr.Detail, "public package dependency is unavailable") {
		return fmt.Errorf(
			"public package dependency is not anonymously downloadable: %w",
			err,
		)
	}
	return err
}

func pushPackageSkills(
	client *cloud.Client,
	compiled *spec.CompiledEnvironment,
	scope string,
	visibility string,
) (map[string]string, error) {
	if compiled == nil {
		return nil, nil
	}
	skills := append([]*spec.Skill{}, compiled.Skills...)
	sort.Slice(skills, func(i, j int) bool {
		if skills[i] == nil {
			return true
		}
		if skills[j] == nil {
			return false
		}
		return skills[i].Name < skills[j].Name
	})
	refs := map[string]string{}
	publishScope := strings.TrimSpace(scope)
	for _, resolved := range skills {
		if resolved == nil || strings.TrimSpace(resolved.Path) == "" {
			continue
		}
		if ref, ok, err := resolvedRegistrySkillRef(client, resolved); err != nil {
			return nil, err
		} else if ok {
			refs[resolved.Name] = ref
			continue
		}
		if ref, ok, err := platformCatalogueSkillRef(client, resolved); err != nil {
			return nil, err
		} else if ok {
			refs[resolved.Name] = ref
			continue
		}
		skill, ok, err := packageSkillDir(resolved.Path, "")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("skill %q is not publishable: %s", resolved.Name, resolved.Path)
		}
		if visibility == "public" && publishScope == "" {
			publishScope, err = defaultPublishScope(client)
			if err != nil {
				return nil, err
			}
		}
		record, err := pushPackageSkill(
			client,
			skill,
			publishScope,
			visibility,
		)
		if err != nil {
			return nil, err
		}
		refs[resolved.Name] = record.Ref
	}
	return refs, nil
}

func defaultPublishScope(client *cloud.Client) (string, error) {
	account, err := client.AccountBootstrap()
	if err != nil {
		return "", fmt.Errorf("resolve default publish scope: %w", err)
	}
	orgID := strings.TrimSpace(client.OrgID)
	if orgID == "" {
		orgID = account.PersonalOrgID
	}
	for _, organization := range account.Organizations {
		if organization.ID != orgID || organization.DefaultPublishScope == nil {
			continue
		}
		scope := strings.TrimSpace(*organization.DefaultPublishScope)
		if scope != "" {
			return scope, nil
		}
	}
	return "", fmt.Errorf("selected organization has no default publish scope")
}

func pushPackageSkill(
	client *cloud.Client,
	skill *skillPackage,
	scope string,
	visibility string,
) (*cloud.SkillRecord, error) {
	if visibility != "public" {
		return pushSkillPackage(client, skill, scope)
	}

	existing, err := client.GetSkill(scope, skill.name)
	if err == nil {
		if existing.Visibility != "public" {
			return nil, privatePublicPackageDependencyError(scope, skill.name)
		}
		return pushSkillPackage(client, skill, scope)
	}
	if !cloud.IsStatus(err, 404) {
		return nil, fmt.Errorf("inspect package skill @%s/%s: %w", scope, skill.name, err)
	}

	record, publishErr := pushSkillPackageWithVisibility(
		client,
		skill,
		scope,
		"public",
	)
	if publishErr == nil {
		return record, nil
	}

	// A concurrent creator or a committed-but-lost response can make the
	// create race look like a failure. Re-read policy, then retry content
	// publication without ever sending initial visibility to an identity that
	// now exists.
	existing, inspectErr := client.GetSkill(scope, skill.name)
	if inspectErr != nil {
		return nil, publishErr
	}
	if existing.Visibility != "public" {
		return nil, privatePublicPackageDependencyError(scope, skill.name)
	}
	return pushSkillPackage(client, skill, scope)
}

func privatePublicPackageDependencyError(scope, name string) error {
	ref := fmt.Sprintf("@%s/%s", scope, name)
	return fmt.Errorf(
		"public package requires %s to be public; change its visibility in Telos before retrying",
		ref,
	)
}

func resolvedRegistrySkillRef(client *cloud.Client, resolved *spec.Skill) (string, bool, error) {
	ref, ok := spec.ParseRegistrySkillRef(resolved.SourceRef)
	if !ok {
		return "", false, nil
	}
	var record *cloud.SkillRecord
	var err error
	if ref.Version == "" {
		record, err = client.GetSkill(ref.Scope, ref.Name)
	} else {
		record, err = client.GetSkillVersion(ref.Scope, ref.Name, ref.Version)
	}
	if err != nil {
		return "", true, fmt.Errorf("resolve registry skill %s: %w", ref.Ref, err)
	}
	if record.Scope != ref.Scope || record.Name != ref.Name {
		return "", true, fmt.Errorf("registry skill %s resolved to %s", ref.Ref, record.Ref)
	}
	digest, _, err := spec.BuildSkillBundle(resolved)
	if err != nil {
		return "", true, fmt.Errorf("bundle registry skill %s: %w", ref.Ref, err)
	}
	if record.Digest != digest {
		return "", true, fmt.Errorf(
			"registry skill %s digest mismatch: local %s, registry %s",
			ref.Ref,
			digest,
			record.Digest,
		)
	}
	return record.Ref, true, nil
}

func platformCatalogueSkillRef(client *cloud.Client, resolved *spec.Skill) (string, bool, error) {
	if !isDefaultCatalogueSkillPath(resolved.Path) {
		return "", false, nil
	}
	digest, _, err := spec.BuildSkillBundle(resolved)
	if err != nil {
		return "", true, fmt.Errorf("bundle platform skill %q: %w", resolved.Name, err)
	}
	record, err := platformSkillRecord(client, resolved)
	if err != nil {
		return "", true, err
	}
	if record.Digest != digest {
		return "", true, fmt.Errorf(
			"platform skill %q digest mismatch: local %s, registry %s (%s)",
			resolved.Name,
			digest,
			record.Digest,
			record.Ref,
		)
	}
	return record.Ref, true, nil
}

func platformSkillRecord(client *cloud.Client, resolved *spec.Skill) (*cloud.SkillRecord, error) {
	scope, name, version, ok := registrySkillRefParts(resolved.SourceRef)
	if ok {
		if scope != "telos" || name != resolved.Name {
			return nil, fmt.Errorf("platform skill %q has inconsistent source ref %q", resolved.Name, resolved.SourceRef)
		}
		if version != "" {
			record, err := client.GetSkillVersion(scope, name, version)
			if err != nil {
				return nil, fmt.Errorf("platform skill %q is not published at %s: %w", resolved.Name, resolved.SourceRef, err)
			}
			return record, nil
		}
	}
	record, err := client.GetSkill("telos", resolved.Name)
	if err != nil {
		return nil, fmt.Errorf("platform skill %q is not published in @telos: %w", resolved.Name, err)
	}
	return record, nil
}

func registrySkillRefParts(raw string) (scope string, name string, version string, ok bool) {
	ref, ok := spec.ParseRegistrySkillRef(raw)
	if !ok {
		return "", "", "", false
	}
	return ref.Scope, ref.Name, ref.Version, true
}

func isDefaultCatalogueSkillPath(path string) bool {
	catalogue := strings.TrimSpace(spec.DefaultSkillsDir())
	if catalogue == "" || strings.TrimSpace(path) == "" {
		return false
	}
	catalogueAbs, err := filepath.Abs(catalogue)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(catalogueAbs, pathAbs)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func pushSkillPackage(client *cloud.Client, skill *skillPackage, scope string) (*cloud.SkillRecord, error) {
	return pushSkillPackageWithVisibility(client, skill, scope, "")
}

func pushSkillPackageWithVisibility(
	client *cloud.Client,
	skill *skillPackage,
	scope string,
	visibility string,
) (*cloud.SkillRecord, error) {
	if skill == nil {
		return nil, fmt.Errorf("skill is required")
	}
	scope = strings.TrimSpace(scope)
	return client.PublishSkillVersionWithVisibility(
		scope,
		skill.name,
		skill.version,
		skill.files,
		visibility,
	)
}

func normalizePackageVersion(raw string) (string, error) {
	version := strings.TrimSpace(raw)
	if version == "" {
		return "", fmt.Errorf("package version is required; set `version: 1.0.0` in SPEC.md frontmatter")
	}
	if strings.HasPrefix(version, "v") {
		return "", fmt.Errorf("package version must not start with v: %s", version)
	}
	suffixAt := strings.IndexAny(version, "-+")
	main := version
	suffix := ""
	if suffixAt >= 0 {
		main = version[:suffixAt]
		suffix = version[suffixAt:]
	}
	if main == "" {
		return "", fmt.Errorf("package version must be semver: %s", version)
	}
	parts := strings.Split(main, ".")
	if len(parts) > 3 {
		return "", fmt.Errorf("package version must be semver: %s", version)
	}
	for _, part := range parts {
		if !packageVersionNumberRE.MatchString(part) {
			return "", fmt.Errorf("package version must be semver: %s", version)
		}
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	normalized := strings.Join(parts, ".") + suffix
	if !packageSemverRE.MatchString(normalized) {
		return "", fmt.Errorf("package version must be semver: %s", version)
	}
	return normalized, nil
}

func printPushReceipt(name string, record *cloud.PackageVersionRecord) {
	fmt.Fprintf(os.Stdout, "pushed %s\n\n", name)
	printSummaryField(os.Stdout, "Ref", record.Ref)
	printSummaryField(os.Stdout, "Digest", record.Digest)
	printSummaryField(os.Stdout, "Version", record.Version)
}

func printSkillPushReceipt(name string, record *cloud.SkillRecord) {
	fmt.Fprintf(os.Stdout, "pushed skill %s\n\n", name)
	printSummaryField(os.Stdout, "Ref", record.Ref)
	printSummaryField(os.Stdout, "Digest", record.Digest)
	printSummaryField(os.Stdout, "Version", record.Version)
	printSummaryField(os.Stdout, "Files", fmt.Sprintf("%d", record.FileCount))
}
