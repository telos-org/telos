package main

import (
	"flag"
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
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	scope := fs.String("scope", "", "Package scope")
	version := fs.String("version", "", "Version override for skill or package publishing")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos push SPEC.md|SKILL_DIR [--scope SCOPE] [--version VERSION] [--json]")
		os.Exit(1)
	}

	input := fs.Arg(0)
	if skill, ok, err := packageSkillDir(input, *version); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	} else if ok {
		client, err := cloud.ControlClient()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		record, err := pushSkillPackage(client, skill, *scope)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(map[string]any{
				"name":  skill.name,
				"skill": record,
			})
			return
		}
		printSkillPushReceipt(skill.name, record)
		return
	}

	pkg, err := packageSpec(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(*version) != "" {
		pkg.version = *version
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
	if err := prepareRegistrySkills(path); err != nil {
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
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill contains non-regular file: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
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
	return pushSpecPackageVersion(client, pkg, scope, pkg.version)
}

func pushSpecPackageVersion(
	client *cloud.Client,
	pkg *specPackage,
	scope string,
	version string,
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
	skillRefs, err := pushPackageSkills(client, pkg.compiled, scope)
	if err != nil {
		return nil, err
	}
	if len(skillRefs) > 0 {
		rebuilt, err := spec.BuildApplyPackageWithSkillRefs(pkg.compiled, skillRefs)
		if err != nil {
			return nil, err
		}
		pkg.digest = rebuilt.Digest
		pkg.bytes = rebuilt.Bytes
	}
	pkg.version = version
	return client.PublishPackage(scope, pkg.name, version, pkg.bytes)
}

func pushPackageSkills(
	client *cloud.Client,
	compiled *spec.CompiledEnvironment,
	scope string,
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
		record, err := pushSkillPackage(client, skill, scope)
		if err != nil {
			return nil, err
		}
		refs[resolved.Name] = record.Ref
	}
	return refs, nil
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
	if skill == nil {
		return nil, fmt.Errorf("skill is required")
	}
	scope = strings.TrimSpace(scope)
	return client.PublishSkillVersion(scope, skill.name, skill.version, skill.files)
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
