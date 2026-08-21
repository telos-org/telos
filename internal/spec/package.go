package spec

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const ApplyPackageSchemaVersion = 1

const (
	applyPackageMaxFiles         = 2048
	applyPackageMaxExpandedBytes = 64 * 1024 * 1024
	applyPackageMaxSpecBytes     = 1024 * 1024
)

// ApplyPackageSchemaVersionStarred gates manifests whose skill locks carry the
// starred (required-rubric) flag. Starred locks change runtime semantics and
// are folded into the package digest; older runtimes would silently drop the
// unknown field and mis-derive the digest, so those packages declare a schema
// version that pre-starred readers reject outright ("unsupported apply
// package schema_version") — an actionable upgrade signal instead of a
// baffling digest mismatch on version-pinned deployments.
const ApplyPackageSchemaVersionStarred = 2

// ApplyPackageSchemaVersionExactRefs identifies registry packages whose skill
// closure is made entirely of canonical, exact @scope/name:version locks. The
// ref is part of package identity in this schema: two different skill
// identities with the same content digest are deliberately different inputs.
const ApplyPackageSchemaVersionExactRefs = 3

func applyPackageSchemaSupported(version int) bool {
	return version == ApplyPackageSchemaVersion ||
		version == ApplyPackageSchemaVersionStarred ||
		version == ApplyPackageSchemaVersionExactRefs
}

func applyPackageSchemaVersionFor(skills map[string]ApplyPackageSkillLock) int {
	for _, lock := range skills {
		if lock.Starred {
			return ApplyPackageSchemaVersionStarred
		}
	}
	return ApplyPackageSchemaVersion
}

// ApplyPackage is an immutable bundle of the root spec and resolved skills.
type ApplyPackage struct {
	Digest   string
	Bytes    []byte
	Manifest ApplyPackageManifest
}

type ApplyPackageSpecEntry struct {
	Digest string `json:"digest"`
}

type ApplyPackageSkillEntry struct {
	Name   string                  `json:"-"`
	Ref    string                  `json:"-"`
	Digest string                  `json:"digest"`
	Files  []ApplyPackageFileEntry `json:"-"`
}

type ApplyPackageSkillLock struct {
	Digest  string `json:"digest"`
	Ref     string `json:"ref,omitempty"`
	Starred bool   `json:"starred,omitempty"`
}

func (lock *ApplyPackageSkillLock) UnmarshalJSON(data []byte) error {
	var digest string
	if err := json.Unmarshal(data, &digest); err == nil {
		lock.Digest = digest
		lock.Ref = ""
		lock.Starred = false
		return nil
	}
	var raw struct {
		Digest  string `json:"digest"`
		Ref     string `json:"ref"`
		Starred bool   `json:"starred"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	lock.Digest = raw.Digest
	lock.Ref = raw.Ref
	lock.Starred = raw.Starred
	return nil
}

type ApplyPackageFileEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Digest string `json:"digest"`
}

type ApplyPackageSkillProvenance struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

type ApplyPackageSkillFetchRequest struct {
	Name   string
	Ref    string
	Digest string
}

type ApplyPackageSkillFetcher func(ApplyPackageSkillFetchRequest) ([]byte, error)

// ApplyPackageManifest records the immutable inputs used by the package.
type ApplyPackageManifest struct {
	SchemaVersion   int                                    `json:"schema_version"`
	Spec            ApplyPackageSpecEntry                  `json:"spec"`
	Skills          map[string]ApplyPackageSkillLock       `json:"skills"`
	SkillProvenance map[string]ApplyPackageSkillProvenance `json:"skill_provenance,omitempty"`
}

func (manifest *ApplyPackageManifest) UnmarshalJSON(data []byte) error {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	if header.SchemaVersion == ApplyPackageSchemaVersionExactRefs {
		var strict struct {
			SchemaVersion int `json:"schema_version"`
			Spec          struct {
				Digest string `json:"digest"`
			} `json:"spec"`
			Skills map[string]struct {
				Digest  string `json:"digest"`
				Ref     string `json:"ref"`
				Starred bool   `json:"starred"`
			} `json:"skills"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&strict); err != nil {
			return err
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			if err == nil {
				return fmt.Errorf("manifest.json contains multiple JSON values")
			}
			return err
		}
		skills := make(map[string]ApplyPackageSkillLock, len(strict.Skills))
		for name, lock := range strict.Skills {
			skills[name] = ApplyPackageSkillLock{
				Digest:  lock.Digest,
				Ref:     lock.Ref,
				Starred: lock.Starred,
			}
		}
		*manifest = ApplyPackageManifest{
			SchemaVersion: strict.SchemaVersion,
			Spec: ApplyPackageSpecEntry{
				Digest: strict.Spec.Digest,
			},
			Skills: skills,
		}
		return nil
	}
	type manifestAlias ApplyPackageManifest
	var decoded manifestAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*manifest = ApplyPackageManifest(decoded)
	return nil
}

// ApplyPackageDigest returns the content address represented by a package manifest.
func ApplyPackageDigest(manifest *ApplyPackageManifest) string {
	if manifest == nil {
		return ""
	}
	return digestPackage(manifest.SchemaVersion, manifest.Spec.Digest, manifest.Skills)
}

// ApplyPackageSpec verifies a package's declared contents and returns its root spec.
// Registry-backed skills may remain external references.
func ApplyPackageSpec(data []byte) ([]byte, *ApplyPackageManifest, error) {
	files, manifest, err := readApplyPackage(data)
	if err != nil {
		return nil, nil, err
	}
	if err := validateApplyPackageFilesAllowReferences(manifest, files); err != nil {
		return nil, nil, err
	}
	if err := validateRegistryApplyPackageShape(manifest, files); err != nil {
		return nil, nil, err
	}
	root := files["SPEC.md"].data
	return append([]byte(nil), root...), manifest, nil
}

type packageFile struct {
	path string
	mode int64
	data []byte
}

// BuildApplyPackage creates a deterministic tar.gz containing the root spec,
// resolved skills, and manifest.json.
func BuildApplyPackage(compiled *CompiledEnvironment) (*ApplyPackage, error) {
	return BuildApplyPackageWithSkillRefs(compiled, nil)
}

// BuildApplyPackageWithSkillRefs builds a package while pinning registry skill
// refs in manifest provenance. Skills with refs are recorded by digest and
// materialized from the registry by telosd.
func BuildApplyPackageWithSkillRefs(compiled *CompiledEnvironment, skillRefs map[string]string) (*ApplyPackage, error) {
	if compiled == nil || compiled.Environment == nil {
		return nil, fmt.Errorf("compiled environment is required")
	}

	specData, err := os.ReadFile(compiled.Environment.Path)
	if err != nil {
		return nil, fmt.Errorf("read root spec: %w", err)
	}
	specEntry := ApplyPackageSpecEntry{
		Digest: digestBytes(specData),
	}

	packageFiles := []packageFile{{
		path: "SPEC.md",
		mode: 0o644,
		data: specData,
	}}
	skillEntries := make([]ApplyPackageSkillEntry, 0, len(compiled.Skills))
	for _, skill := range sortedSkills(compiled.Skills) {
		registryRef := ""
		if skill != nil {
			rawRef := skillRefs[skill.Name]
			registryRef = strings.TrimSpace(rawRef)
			if skillRefs != nil {
				if rawRef != registryRef {
					return nil, fmt.Errorf("registry skill ref for %q must be canonical", skill.Name)
				}
				parsed, ok := ParseRegistrySkillRef(registryRef)
				if !ok || parsed.Version == "" || parsed.Ref != registryRef {
					return nil, fmt.Errorf("registry skill ref for %q must be exact and canonical", skill.Name)
				}
				if parsed.Name != skill.Name {
					return nil, fmt.Errorf("registry skill ref for %q must use the same skill name", skill.Name)
				}
			}
		}
		entry, files, err := packageSkill(
			skill,
			compiled.Environment.Path,
			registryRef,
			registryRef == "",
		)
		if err != nil {
			return nil, err
		}
		skillEntries = append(skillEntries, entry)
		packageFiles = append(packageFiles, files...)
	}

	requiredNames := make(map[string]bool, len(compiled.RequiredVerifierSkills))
	for _, skill := range compiled.RequiredVerifierSkills {
		if skill != nil {
			requiredNames[skill.Name] = true
		}
	}
	skillLocks := skillLockMap(skillEntries, requiredNames)
	schemaVersion := applyPackageSchemaVersionFor(skillLocks)
	if skillRefs != nil {
		schemaVersion = ApplyPackageSchemaVersionExactRefs
	}
	manifest := ApplyPackageManifest{
		SchemaVersion: schemaVersion,
		Spec:          specEntry,
		Skills:        skillLocks,
	}
	packageDigest := digestPackage(manifest.SchemaVersion, specEntry.Digest, manifest.Skills)

	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	packageFiles = append(packageFiles, packageFile{path: "manifest.json", mode: 0o644, data: manifestData})

	data, err := writePackageTar(packageFiles)
	if err != nil {
		return nil, err
	}
	return &ApplyPackage{
		Digest:   packageDigest,
		Bytes:    data,
		Manifest: manifest,
	}, nil
}

// ExtractApplyPackage expands an apply package into dest and returns its manifest.
func ExtractApplyPackage(data []byte, dest string) (*ApplyPackageManifest, error) {
	files, manifest, err := readApplyPackage(data)
	if err != nil {
		return nil, err
	}
	if err := validateApplyPackageFiles(manifest, files); err != nil {
		return nil, err
	}
	if err := writePackageFiles(dest, files); err != nil {
		return nil, err
	}
	return manifest, nil
}

// HydrateApplyPackage returns a self-contained package tarball by fetching any
// registry-backed skill blobs referenced by the package manifest.
func HydrateApplyPackage(data []byte, fetch ApplyPackageSkillFetcher) ([]byte, *ApplyPackageManifest, error) {
	files, manifest, err := readApplyPackage(data)
	if err != nil {
		return nil, nil, err
	}
	if err := validateApplyPackageFilesAllowReferences(manifest, files); err != nil {
		return nil, nil, err
	}
	if err := validateRegistryApplyPackageShape(manifest, files); err != nil {
		return nil, nil, err
	}
	for _, name := range missingPackageSkillNames(manifest, files) {
		if fetch == nil {
			return nil, nil, fmt.Errorf("fetch skill %q: skill fetcher is required", name)
		}
		provenance, ok := manifest.SkillProvenance[name]
		ref := strings.TrimSpace(manifest.skillRef(name))
		if ref == "" && ok {
			ref = strings.TrimSpace(provenance.Ref)
		}
		if ref == "" {
			return nil, nil, fmt.Errorf("package missing registry ref for skill %q", name)
		}
		expectedDigest := manifest.skillDigest(name)
		bundle, err := fetch(ApplyPackageSkillFetchRequest{
			Name:   name,
			Ref:    ref,
			Digest: expectedDigest,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("fetch skill %q: %w", name, err)
		}
		skillFiles, err := readSkillBundleFiles(name, bundle)
		if err != nil {
			return nil, nil, err
		}
		if digest := digestPackagedSkill(name, skillFiles); digest != expectedDigest {
			return nil, nil, fmt.Errorf("skill digest mismatch for %q: got %s want %s", name, digest, expectedDigest)
		}
		skillPathName, err := packagePathName(name)
		if err != nil {
			return nil, nil, err
		}
		for _, file := range skillFiles {
			path := filepath.ToSlash(filepath.Join("skills", skillPathName, file.path))
			if _, exists := files[path]; exists {
				return nil, nil, fmt.Errorf("duplicate apply package entry %q", path)
			}
			files[path] = packageFile{path: path, mode: file.mode, data: file.data}
		}
	}
	if err := validateApplyPackageFiles(manifest, files); err != nil {
		return nil, nil, err
	}
	hydrated, err := writePackageTar(packageFilesFromMap(files))
	if err != nil {
		return nil, nil, err
	}
	return hydrated, manifest, nil
}

func BuildSkillBundle(skill *Skill) (string, []byte, error) {
	if skill == nil {
		return "", nil, fmt.Errorf("nil skill")
	}
	if strings.TrimSpace(skill.Name) == "" {
		return "", nil, fmt.Errorf("skill with empty name")
	}
	if _, err := packagePathName(skill.Name); err != nil {
		return "", nil, err
	}
	files, err := readSkillFiles(skill)
	if err != nil {
		return "", nil, err
	}
	if !packageFilesContainSkillMarkdown(files) {
		return "", nil, fmt.Errorf("skill %q missing SKILL.md", skill.Name)
	}
	digest := digestPackagedSkill(skill.Name, files)
	data, err := writePackageTar(files)
	if err != nil {
		return "", nil, err
	}
	return digest, data, nil
}

func VerifySkillBundle(name string, expectedDigest string, data []byte) error {
	files, err := readSkillBundleFiles(name, data)
	if err != nil {
		return err
	}
	if digest := digestPackagedSkill(name, files); digest != expectedDigest {
		return fmt.Errorf("skill digest mismatch for %q: got %s want %s", name, digest, expectedDigest)
	}
	return nil
}

// ExtractSkillBundle verifies and expands a registry skill bundle into dest.
func ExtractSkillBundle(name string, expectedDigest string, data []byte, dest string) error {
	files, err := readSkillBundleFiles(name, data)
	if err != nil {
		return err
	}
	if digest := digestPackagedSkill(name, files); digest != expectedDigest {
		return fmt.Errorf("skill digest mismatch for %q: got %s want %s", name, digest, expectedDigest)
	}
	entries := make(map[string]packageFile, len(files))
	for _, file := range files {
		if _, exists := entries[file.path]; exists {
			return fmt.Errorf("duplicate skill bundle entry %q", file.path)
		}
		entries[file.path] = file
	}
	return writePackageFiles(dest, entries)
}

func readApplyPackage(data []byte) (map[string]packageFile, *ApplyPackageManifest, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("open apply package: %w", err)
	}
	defer gz.Close()

	var manifestData []byte
	files := map[string]packageFile{}
	var expandedBytes int64
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read apply package: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			return nil, nil, fmt.Errorf("unsupported apply package entry %q", header.Name)
		}
		if len(files) >= applyPackageMaxFiles {
			return nil, nil, fmt.Errorf("apply package contains more than %d files", applyPackageMaxFiles)
		}
		name, err := safePackageEntry(header.Name)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := files[name]; exists {
			return nil, nil, fmt.Errorf("duplicate apply package entry %q", name)
		}
		if header.Size < 0 {
			return nil, nil, fmt.Errorf("invalid apply package entry size %q", name)
		}
		expandedBytes += header.Size
		if expandedBytes > applyPackageMaxExpandedBytes {
			return nil, nil, fmt.Errorf("apply package expanded content exceeds %d bytes", applyPackageMaxExpandedBytes)
		}
		if name == "SPEC.md" && header.Size > applyPackageMaxSpecBytes {
			return nil, nil, fmt.Errorf("apply package SPEC.md exceeds %d bytes", applyPackageMaxSpecBytes)
		}
		fileData, err := io.ReadAll(io.LimitReader(tr, header.Size+1))
		if err != nil {
			return nil, nil, fmt.Errorf("read apply package entry %q: %w", name, err)
		}
		if int64(len(fileData)) != header.Size {
			return nil, nil, fmt.Errorf("apply package entry size mismatch %q", name)
		}
		mode := fs.FileMode(header.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		files[name] = packageFile{path: name, mode: int64(mode), data: fileData}
		if name == "manifest.json" {
			manifestData = fileData
		}
	}
	if len(manifestData) == 0 {
		return nil, nil, fmt.Errorf("apply package missing manifest.json")
	}
	var manifest ApplyPackageManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parse manifest.json: %w", err)
	}
	return files, &manifest, nil
}

func writePackageFiles(dest string, files map[string]packageFile) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create package dir: %w", err)
	}
	for name, file := range files {
		path := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create package entry dir: %w", err)
		}
		if err := os.WriteFile(path, file.data, fs.FileMode(file.mode)); err != nil {
			return fmt.Errorf("write package entry %q: %w", name, err)
		}
	}
	return nil
}

func validateApplyPackageFiles(manifest *ApplyPackageManifest, files map[string]packageFile) error {
	return validateApplyPackageFilesWithMode(manifest, files, false)
}

func validateApplyPackageFilesAllowReferences(manifest *ApplyPackageManifest, files map[string]packageFile) error {
	return validateApplyPackageFilesWithMode(manifest, files, true)
}

func validateApplyPackageFilesWithMode(manifest *ApplyPackageManifest, files map[string]packageFile, allowReferencedSkills bool) error {
	if manifest == nil {
		return fmt.Errorf("apply package missing manifest")
	}
	if !applyPackageSchemaSupported(manifest.SchemaVersion) {
		return fmt.Errorf("unsupported apply package schema_version %d", manifest.SchemaVersion)
	}
	if manifest.SchemaVersion < ApplyPackageSchemaVersionStarred {
		for _, lock := range manifest.Skills {
			if lock.Starred {
				// Mirrors the Cloud publish rule: a schema-1 starred lock
				// would be silently dropped (and its digest mis-derived) by
				// pre-starred readers, so the combination is invalid even for
				// locally supplied packages that never pass through Cloud.
				return fmt.Errorf(
					"starred skill locks require schema_version %d",
					ApplyPackageSchemaVersionStarred,
				)
			}
		}
	}
	if manifest.SchemaVersion == ApplyPackageSchemaVersionExactRefs {
		if len(manifest.SkillProvenance) != 0 {
			return fmt.Errorf("schema_version %d does not allow skill_provenance", ApplyPackageSchemaVersionExactRefs)
		}
		for name, lock := range manifest.Skills {
			ref := strings.TrimSpace(lock.Ref)
			parsed, ok := ParseRegistrySkillRef(ref)
			if !ok || parsed.Version == "" || parsed.Ref != ref || lock.Ref != ref {
				return fmt.Errorf("schema_version %d skill %q must use an exact canonical ref", ApplyPackageSchemaVersionExactRefs, name)
			}
			if parsed.Name != name {
				return fmt.Errorf("schema_version %d skill %q must match ref name %q", ApplyPackageSchemaVersionExactRefs, name, parsed.Name)
			}
		}
	}
	specFile, ok := files["SPEC.md"]
	if !ok {
		return fmt.Errorf("apply package missing SPEC.md")
	}
	if manifest.Spec.Digest == "" {
		return fmt.Errorf("manifest.json missing spec digest")
	}
	specDigest := digestBytes(specFile.data)
	if specDigest != manifest.Spec.Digest {
		return fmt.Errorf("spec digest mismatch: got %s want %s", specDigest, manifest.Spec.Digest)
	}
	if manifest.Skills == nil {
		return fmt.Errorf("manifest.json missing skills")
	}
	skillEntries := map[string][]ApplyPackageFileEntry{}
	skillNames := make([]string, 0, len(manifest.Skills))
	for name := range manifest.Skills {
		if _, err := packagePathName(name); err != nil {
			return err
		}
		skillNames = append(skillNames, name)
	}
	sort.Strings(skillNames)
	for pathName, file := range files {
		if pathName == "SPEC.md" || pathName == "manifest.json" {
			continue
		}
		matched := false
		for _, skillName := range skillNames {
			prefix := "skills/" + skillName + "/"
			if !strings.HasPrefix(pathName, prefix) {
				continue
			}
			rel := strings.TrimPrefix(pathName, prefix)
			if rel == "" {
				return fmt.Errorf("unsafe apply package entry %q", pathName)
			}
			skillEntries[skillName] = append(skillEntries[skillName], ApplyPackageFileEntry{
				Path:   rel,
				Mode:   fmt.Sprintf("%04o", file.mode),
				Digest: digestFile(rel, file.mode, file.data),
			})
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("apply package contains unmanifested file %q", pathName)
		}
	}
	for _, skillName := range skillNames {
		entries := skillEntries[skillName]
		if len(entries) == 0 {
			if allowReferencedSkills && packageSkillHasRegistryRef(manifest, skillName) {
				continue
			}
			return fmt.Errorf("apply package missing skill files for %q", skillName)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		skillDigest := digestSkill(skillName, entries)
		if skillDigest != manifest.skillDigest(skillName) {
			return fmt.Errorf("skill digest mismatch for %q: got %s want %s", skillName, skillDigest, manifest.skillDigest(skillName))
		}
	}
	return nil
}

func validateRegistryApplyPackageShape(manifest *ApplyPackageManifest, files map[string]packageFile) error {
	if manifest == nil || manifest.SchemaVersion != ApplyPackageSchemaVersionExactRefs {
		return nil
	}
	for name := range manifest.Skills {
		prefix := "skills/" + name + "/"
		for path := range files {
			if strings.HasPrefix(path, prefix) {
				return fmt.Errorf("schema_version %d skill %q must not embed skill files", ApplyPackageSchemaVersionExactRefs, name)
			}
		}
	}
	return nil
}

func packageSkillHasRegistryRef(manifest *ApplyPackageManifest, name string) bool {
	if manifest == nil {
		return false
	}
	if skill, ok := manifest.Skills[name]; ok {
		ref := strings.TrimSpace(skill.Ref)
		if ref != "" {
			return strings.HasPrefix(ref, "@") && strings.TrimSpace(skill.Digest) != ""
		}
	}
	if manifest.SkillProvenance == nil {
		return false
	}
	provenance, ok := manifest.SkillProvenance[name]
	ref := strings.TrimSpace(provenance.Ref)
	return ok &&
		strings.HasPrefix(ref, "@") &&
		provenance.Digest == manifest.skillDigest(name)
}

func missingPackageSkillNames(manifest *ApplyPackageManifest, files map[string]packageFile) []string {
	if manifest == nil {
		return nil
	}
	names := make([]string, 0, len(manifest.Skills))
	for name := range manifest.Skills {
		prefix := "skills/" + name + "/"
		found := false
		for path := range files {
			if strings.HasPrefix(path, prefix) {
				found = true
				break
			}
		}
		if !found {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func packageSkill(
	skill *Skill,
	rootSpecPath string,
	registryRef string,
	includeFiles bool,
) (ApplyPackageSkillEntry, []packageFile, error) {
	if skill == nil {
		return ApplyPackageSkillEntry{}, nil, fmt.Errorf("nil skill")
	}
	if strings.TrimSpace(skill.Name) == "" {
		return ApplyPackageSkillEntry{}, nil, fmt.Errorf("skill with empty name")
	}
	skillPathName, err := packagePathName(skill.Name)
	if err != nil {
		return ApplyPackageSkillEntry{}, nil, err
	}
	if strings.TrimSpace(skill.Path) == "" {
		return ApplyPackageSkillEntry{}, nil, fmt.Errorf("skill %q has no source path", skill.Name)
	}
	files, err := readSkillFiles(skill)
	if err != nil {
		return ApplyPackageSkillEntry{}, nil, err
	}
	fileEntries := make([]ApplyPackageFileEntry, 0, len(files))
	packaged := make([]packageFile, 0, len(files))
	for _, file := range files {
		fileDigest := digestFile(file.path, file.mode, file.data)
		fileEntries = append(fileEntries, ApplyPackageFileEntry{
			Path:   file.path,
			Mode:   fmt.Sprintf("%04o", file.mode),
			Digest: fileDigest,
		})
		if includeFiles {
			packaged = append(packaged, packageFile{
				path: filepath.ToSlash(filepath.Join("skills", skillPathName, file.path)),
				mode: file.mode,
				data: file.data,
			})
		}
	}
	ref := strings.TrimSpace(registryRef)
	if ref == "" {
		ref = skillSourceRef(skill, rootSpecPath)
	}
	entry := ApplyPackageSkillEntry{
		Name:   skill.Name,
		Ref:    ref,
		Digest: digestSkill(skill.Name, fileEntries),
		Files:  fileEntries,
	}
	return entry, packaged, nil
}

func readSkillBundleFiles(name string, data []byte) ([]packageFile, error) {
	if _, err := packagePathName(name); err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open skill bundle %q: %w", name, err)
	}
	defer gz.Close()

	files := []packageFile{}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read skill bundle %q: %w", name, err)
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unsupported skill bundle entry %q", header.Name)
		}
		path, err := safePackageEntry(header.Name)
		if err != nil {
			return nil, err
		}
		fileData, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read skill bundle entry %q: %w", path, err)
		}
		mode := fs.FileMode(header.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		files = append(files, packageFile{
			path: path,
			mode: int64(normalizedPackageMode(mode)),
			data: fileData,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	if !packageFilesContainSkillMarkdown(files) {
		return nil, fmt.Errorf("skill bundle %q missing SKILL.md", name)
	}
	return files, nil
}

func packageFilesContainSkillMarkdown(files []packageFile) bool {
	for _, file := range files {
		if file.path == "SKILL.md" {
			return true
		}
	}
	return false
}

func digestPackagedSkill(name string, files []packageFile) string {
	entries := make([]ApplyPackageFileEntry, 0, len(files))
	for _, file := range files {
		entries = append(entries, ApplyPackageFileEntry{
			Path:   file.path,
			Mode:   fmt.Sprintf("%04o", file.mode),
			Digest: digestFile(file.path, file.mode, file.data),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return digestSkill(name, entries)
}

func packageFilesFromMap(files map[string]packageFile) []packageFile {
	out := make([]packageFile, 0, len(files))
	for _, file := range files {
		out = append(out, file)
	}
	return out
}

func safePackageEntry(name string) (string, error) {
	if name == "" || name != strings.TrimSpace(name) || strings.Contains(name, `\`) || strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || strings.Contains(name, "//") {
		return "", fmt.Errorf("unsafe apply package entry %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("unsafe apply package entry %q", name)
		}
	}
	return name, nil
}

func packagePathName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("skill %q is not safe as a package path", name)
	}
	return name, nil
}

func readSkillFiles(skill *Skill) ([]packageFile, error) {
	root := skill.Path
	var files []packageFile
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
			return fmt.Errorf("skill %q contains non-regular file: %s", skill.Name, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, packageFile{
			path: filepath.ToSlash(rel),
			mode: normalizedPackageMode(info.Mode()),
			data: data,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read skill %q files: %w", skill.Name, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func normalizedPackageMode(mode fs.FileMode) int64 {
	if mode.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func sortedSkills(skills []*Skill) []*Skill {
	out := append([]*Skill{}, skills...)
	sort.Slice(out, func(i, j int) bool {
		if out[i] == nil {
			return true
		}
		if out[j] == nil {
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func digestFile(path string, mode int64, data []byte) string {
	h := sha256.New()
	writeDigestPart(h, filepath.ToSlash(path))
	writeDigestPart(h, fmt.Sprintf("%04o", mode))
	h.Write(data)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

func digestSkill(name string, files []ApplyPackageFileEntry) string {
	h := sha256.New()
	writeDigestPart(h, name)
	for _, file := range files {
		writeDigestPart(h, file.Path)
		writeDigestPart(h, file.Mode)
		writeDigestPart(h, file.Digest)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

func skillLockMap(skills []ApplyPackageSkillEntry, requiredNames map[string]bool) map[string]ApplyPackageSkillLock {
	out := make(map[string]ApplyPackageSkillLock, len(skills))
	for _, skill := range skills {
		out[skill.Name] = ApplyPackageSkillLock{
			Digest:  skill.Digest,
			Ref:     strings.TrimSpace(skill.Ref),
			Starred: requiredNames[skill.Name],
		}
	}
	return out
}

func skillSourceRef(skill *Skill, rootSpecPath string) string {
	if skill == nil || strings.TrimSpace(skill.Path) == "" {
		return ""
	}
	path := skill.Path
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if catalog := DefaultSkillsDir(); catalog != "" {
		if rel, ok := relativePathWithin(catalog, path); ok {
			return "catalog:" + rel
		}
	}
	if rootSpecPath != "" {
		if rel, err := filepath.Rel(filepath.Dir(rootSpecPath), path); err == nil {
			return "path:" + filepath.ToSlash(rel)
		}
	}
	return "path:" + filepath.ToSlash(path)
}

func relativePathWithin(root string, path string) (string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func digestPackage(schemaVersion int, specDigest string, skills map[string]ApplyPackageSkillLock) string {
	h := sha256.New()
	writeDigestPart(h, fmt.Sprintf("schema:%d", schemaVersion))
	writeDigestPart(h, "spec:"+specDigest)
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		// Starred marks a required evaluation rubric — runtime semantics, so
		// it is part of package identity. The marker perturbs the hash only
		// when set (skill names cannot contain '*'), keeping every previously
		// published digest stable.
		if skills[name].Starred {
			writeDigestPart(h, name+"*")
		} else {
			writeDigestPart(h, name)
		}
		if schemaVersion == ApplyPackageSchemaVersionExactRefs {
			writeDigestPart(h, skills[name].Ref)
		}
		writeDigestPart(h, skills[name].Digest)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

func (manifest *ApplyPackageManifest) skillDigest(name string) string {
	if manifest == nil || manifest.Skills == nil {
		return ""
	}
	return strings.TrimSpace(manifest.Skills[name].Digest)
}

func (manifest *ApplyPackageManifest) skillRef(name string) string {
	if manifest == nil || manifest.Skills == nil {
		return ""
	}
	ref := strings.TrimSpace(manifest.Skills[name].Ref)
	if ref != "" {
		return ref
	}
	if manifest.SkillProvenance == nil {
		return ""
	}
	return strings.TrimSpace(manifest.SkillProvenance[name].Ref)
}

func writeDigestPart(w io.Writer, value string) {
	_, _ = io.WriteString(w, value)
	_, _ = w.Write([]byte{0})
}

func writePackageTar(files []packageFile) ([]byte, error) {
	sorted := append([]packageFile{}, files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Header.ModTime = time.Unix(0, 0).UTC()
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, file := range sorted {
		header := &tar.Header{
			Name:       filepath.ToSlash(file.path),
			Mode:       file.mode,
			Size:       int64(len(file.data)),
			ModTime:    time.Unix(0, 0).UTC(),
			AccessTime: time.Unix(0, 0).UTC(),
			ChangeTime: time.Unix(0, 0).UTC(),
			Typeflag:   tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
