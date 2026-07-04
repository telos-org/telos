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

// ApplyPackageOptions controls deterministic spec+skill package creation.
type ApplyPackageOptions struct {
	CompilerVersion string
	RuntimeVersion  string
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
	Origin string                  `json:"-"`
	Digest string                  `json:"digest"`
	Files  []ApplyPackageFileEntry `json:"-"`
}

type ApplyPackageFileEntry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Digest string `json:"digest"`
}

type ApplyPackageSkillProvenance struct {
	Ref    string `json:"ref,omitempty"`
	Origin string `json:"origin,omitempty"`
	Digest string `json:"digest"`
}

// ApplyPackageManifest records the immutable inputs used by the package.
type ApplyPackageManifest struct {
	SchemaVersion   int                                    `json:"schema_version"`
	Spec            ApplyPackageSpecEntry                  `json:"spec"`
	Skills          map[string]string                      `json:"skills"`
	SkillProvenance map[string]ApplyPackageSkillProvenance `json:"skill_provenance,omitempty"`
	Compiler        string                                 `json:"compiler,omitempty"`
	Runtime         string                                 `json:"runtime,omitempty"`
}

type packageFile struct {
	path string
	mode int64
	data []byte
}

// BuildApplyPackage creates a deterministic tar.gz containing the root spec,
// resolved skills, and manifest.json.
func BuildApplyPackage(compiled *CompiledEnvironment, opts ApplyPackageOptions) (*ApplyPackage, error) {
	if compiled == nil || compiled.Environment == nil {
		return nil, fmt.Errorf("compiled environment is required")
	}
	compilerVersion := strings.TrimSpace(opts.CompilerVersion)
	if compilerVersion == "" {
		compilerVersion = "dev"
	}

	specData, err := os.ReadFile(compiled.Environment.Path)
	if err != nil {
		return nil, fmt.Errorf("read root spec: %w", err)
	}
	specEntry := ApplyPackageSpecEntry{
		Digest: digestBytes(specData),
	}

	required := map[string]bool{}
	for _, skill := range compiled.RequiredVerifierSkills {
		required[skill.Name] = true
	}

	packageFiles := []packageFile{{
		path: "SPEC.md",
		mode: 0o644,
		data: specData,
	}}
	skillEntries := make([]ApplyPackageSkillEntry, 0, len(compiled.Skills))
	for _, skill := range sortedSkills(compiled.Skills) {
		entry, files, err := packageSkill(skill, compiled.Environment.Path, required[skill.Name])
		if err != nil {
			return nil, err
		}
		skillEntries = append(skillEntries, entry)
		packageFiles = append(packageFiles, files...)
	}

	runtimeVersion := strings.TrimSpace(opts.RuntimeVersion)
	manifest := ApplyPackageManifest{
		SchemaVersion:   ApplyPackageSchemaVersion,
		Spec:            specEntry,
		Skills:          skillDigestMap(skillEntries),
		SkillProvenance: skillProvenanceMap(skillEntries),
		Compiler:        "telos@" + compilerVersion,
		Runtime:         runtimeVersion,
	}
	packageDigest := digestPackage(specEntry.Digest, manifest.Skills)

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
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, fmt.Errorf("create package dir: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open apply package: %w", err)
	}
	defer gz.Close()

	var manifestData []byte
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read apply package: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unsupported apply package entry %q", header.Name)
		}
		name, err := safePackageEntry(header.Name)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create package entry dir: %w", err)
		}
		fileData, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read apply package entry %q: %w", name, err)
		}
		mode := fs.FileMode(header.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(path, fileData, mode); err != nil {
			return nil, fmt.Errorf("write apply package entry %q: %w", name, err)
		}
		if name == "manifest.json" {
			manifestData = fileData
		}
	}
	if len(manifestData) == 0 {
		return nil, fmt.Errorf("apply package missing manifest.json")
	}
	var manifest ApplyPackageManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest.json: %w", err)
	}
	if manifest.Spec.Digest == "" {
		return nil, fmt.Errorf("manifest.json missing spec digest")
	}
	return &manifest, nil
}

func packageSkill(skill *Skill, rootSpecPath string, required bool) (ApplyPackageSkillEntry, []packageFile, error) {
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
		packaged = append(packaged, packageFile{
			path: filepath.ToSlash(filepath.Join("skills", skillPathName, file.path)),
			mode: file.mode,
			data: file.data,
		})
	}
	entry := ApplyPackageSkillEntry{
		Name:   skill.Name,
		Ref:    skillSourceRef(skill, rootSpecPath),
		Origin: skillOrigin(skill, required),
		Digest: digestSkill(skill.Name, fileEntries),
		Files:  fileEntries,
	}
	_ = required
	return entry, packaged, nil
}

func safePackageEntry(name string) (string, error) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	if name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return "", fmt.Errorf("unsafe apply package entry %q", name)
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
			mode: int64(info.Mode().Perm()),
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

func skillDigestMap(skills []ApplyPackageSkillEntry) map[string]string {
	out := make(map[string]string, len(skills))
	for _, skill := range skills {
		out[skill.Name] = skill.Digest
	}
	return out
}

func skillProvenanceMap(skills []ApplyPackageSkillEntry) map[string]ApplyPackageSkillProvenance {
	out := make(map[string]ApplyPackageSkillProvenance, len(skills))
	for _, skill := range skills {
		out[skill.Name] = ApplyPackageSkillProvenance{
			Ref:    skill.Ref,
			Origin: skill.Origin,
			Digest: skill.Digest,
		}
	}
	return out
}

func skillOrigin(skill *Skill, required bool) string {
	if required {
		return "required_verifier"
	}
	if isDefaultVerifierSkill(skill) {
		return "platform"
	}
	return "declared"
}

func isDefaultVerifierSkill(skill *Skill) bool {
	if skill == nil {
		return false
	}
	for _, name := range DefaultVerifierSkills {
		if skill.Name == name {
			return true
		}
	}
	return false
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

func digestPackage(specDigest string, skills map[string]string) string {
	h := sha256.New()
	writeDigestPart(h, fmt.Sprintf("schema:%d", ApplyPackageSchemaVersion))
	writeDigestPart(h, "spec:"+specDigest)
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writeDigestPart(h, name)
		writeDigestPart(h, skills[name])
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
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
