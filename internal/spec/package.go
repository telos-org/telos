package spec

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	Lock     ApplyPackageLock
}

// ApplyPackageManifest is written to manifest.yaml inside the package.
type ApplyPackageManifest struct {
	SchemaVersion   int                      `yaml:"schema_version"`
	RootSpecPath    string                   `yaml:"root_spec_path"`
	Spec            ApplyPackageSpecEntry    `yaml:"spec"`
	Skills          []ApplyPackageSkillEntry `yaml:"skills"`
	CompilerVersion string                   `yaml:"compiler_version"`
	RuntimeVersion  string                   `yaml:"runtime_version,omitempty"`
	PackageDigest   string                   `yaml:"package_digest"`
}

type ApplyPackageSpecEntry struct {
	Name   string `yaml:"name"`
	Path   string `yaml:"path"`
	Digest string `yaml:"digest"`
}

type ApplyPackageSkillEntry struct {
	Name     string                  `yaml:"name"`
	Ref      string                  `yaml:"ref"`
	Digest   string                  `yaml:"digest"`
	Required bool                    `yaml:"required"`
	Path     string                  `yaml:"path"`
	Files    []ApplyPackageFileEntry `yaml:"files"`
}

type ApplyPackageFileEntry struct {
	Path   string `yaml:"path"`
	Mode   string `yaml:"mode"`
	Digest string `yaml:"digest"`
}

// ApplyPackageLock records the immutable inputs used by the package.
type ApplyPackageLock struct {
	SchemaVersion int                      `yaml:"schema_version"`
	PackageDigest string                   `yaml:"package_digest"`
	Spec          ApplyPackageSpecEntry    `yaml:"spec"`
	Skills        []ApplyPackageSkillEntry `yaml:"skills"`
}

type packageFile struct {
	path string
	mode int64
	data []byte
}

// BuildApplyPackage creates a deterministic tar.gz containing the root spec,
// resolved skills, manifest.yaml, and lock.yaml.
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
		Name:   compiled.Environment.Name,
		Path:   "specs/main/SPEC.md",
		Digest: digestBytes(specData),
	}

	required := map[string]bool{}
	for _, skill := range compiled.RequiredVerifierSkills {
		required[skill.Name] = true
	}

	packageFiles := []packageFile{{
		path: specEntry.Path,
		mode: 0o644,
		data: specData,
	}}
	skillEntries := make([]ApplyPackageSkillEntry, 0, len(compiled.Skills))
	for _, skill := range sortedSkills(compiled.Skills) {
		entry, files, err := packageSkill(skill, required[skill.Name])
		if err != nil {
			return nil, err
		}
		skillEntries = append(skillEntries, entry)
		packageFiles = append(packageFiles, files...)
	}

	runtimeVersion := strings.TrimSpace(opts.RuntimeVersion)
	packageDigest := digestPackage(specEntry.Digest, skillEntries, compilerVersion, runtimeVersion)
	manifest := ApplyPackageManifest{
		SchemaVersion:   ApplyPackageSchemaVersion,
		RootSpecPath:    specEntry.Path,
		Spec:            specEntry,
		Skills:          skillEntries,
		CompilerVersion: compilerVersion,
		RuntimeVersion:  runtimeVersion,
		PackageDigest:   packageDigest,
	}
	lock := ApplyPackageLock{
		SchemaVersion: ApplyPackageSchemaVersion,
		PackageDigest: packageDigest,
		Spec:          specEntry,
		Skills:        skillEntries,
	}

	manifestData, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	lockData, err := yaml.Marshal(lock)
	if err != nil {
		return nil, fmt.Errorf("marshal lock: %w", err)
	}
	packageFiles = append(packageFiles,
		packageFile{path: "manifest.yaml", mode: 0o644, data: manifestData},
		packageFile{path: "lock.yaml", mode: 0o644, data: lockData},
	)

	data, err := writePackageTar(packageFiles)
	if err != nil {
		return nil, err
	}
	return &ApplyPackage{
		Digest:   packageDigest,
		Bytes:    data,
		Manifest: manifest,
		Lock:     lock,
	}, nil
}

func packageSkill(skill *Skill, required bool) (ApplyPackageSkillEntry, []packageFile, error) {
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
		Name:     skill.Name,
		Ref:      skill.Name,
		Digest:   digestSkill(skill.Name, fileEntries),
		Required: required,
		Path:     filepath.ToSlash(filepath.Join("skills", skillPathName)),
		Files:    fileEntries,
	}
	return entry, packaged, nil
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

func digestPackage(specDigest string, skills []ApplyPackageSkillEntry, compilerVersion, runtimeVersion string) string {
	h := sha256.New()
	writeDigestPart(h, fmt.Sprintf("schema:%d", ApplyPackageSchemaVersion))
	writeDigestPart(h, "compiler:"+compilerVersion)
	writeDigestPart(h, "runtime:"+runtimeVersion)
	writeDigestPart(h, "spec:"+specDigest)
	sorted := append([]ApplyPackageSkillEntry{}, skills...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, skill := range sorted {
		writeDigestPart(h, skill.Name)
		writeDigestPart(h, skill.Digest)
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
