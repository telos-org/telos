package spec

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestBuildApplyPackageIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-deterministic", "alpha")
	writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md":              "---\nname: alpha\ndescription: Alpha\n---\nUse alpha.",
		"reference/example.txt": "example",
	})

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}

	first, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage first: %v", err)
	}
	second, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage second: %v", err)
	}

	if first.Digest != second.Digest {
		t.Fatalf("digest changed: %s != %s", first.Digest, second.Digest)
	}
	if !bytes.Equal(first.Bytes, second.Bytes) {
		t.Fatal("package bytes changed for identical inputs")
	}
	if first.Manifest.Spec.Digest == "" {
		t.Fatalf("manifest missing spec digest: %#v", first.Manifest)
	}
	if first.Manifest.Skills["alpha"] == "" {
		t.Fatalf("manifest missing alpha skill digest: %#v", first.Manifest.Skills)
	}
	if first.Manifest.SkillProvenance["alpha"].Digest != first.Manifest.Skills["alpha"] {
		t.Fatalf("manifest alpha provenance mismatch: %#v", first.Manifest.SkillProvenance["alpha"])
	}
	if first.Manifest.SkillProvenance["alpha"].Ref != "path:alpha" {
		t.Fatalf("manifest alpha provenance ref: got %q", first.Manifest.SkillProvenance["alpha"].Ref)
	}

	entries := tarEntries(t, first.Bytes)
	for _, want := range []string{
		"manifest.json",
		"SPEC.md",
		"skills/alpha/SKILL.md",
		"skills/alpha/reference/example.txt",
	} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("missing package entry %q; entries=%v", want, sortedEntryNames(entries))
		}
	}
	var manifest map[string]any
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if _, ok := manifest["package_digest"]; ok {
		t.Fatalf("manifest should not contain package_digest: %#v", manifest)
	}
	if _, ok := manifest["root_spec_path"]; ok {
		t.Fatalf("manifest should not contain root_spec_path: %#v", manifest)
	}
	if _, ok := manifest["compiler"]; ok {
		t.Fatalf("manifest should not contain compiler provenance: %#v", manifest)
	}
	if _, ok := manifest["runtime"]; ok {
		t.Fatalf("manifest should not contain runtime provenance: %#v", manifest)
	}
	rawProvenance, ok := manifest["skill_provenance"].(map[string]any)
	if !ok {
		t.Fatalf("manifest missing skill_provenance: %#v", manifest)
	}
	alpha, ok := rawProvenance["alpha"].(map[string]any)
	if !ok {
		t.Fatalf("manifest missing alpha skill_provenance: %#v", rawProvenance)
	}
	if alpha["digest"] != first.Manifest.Skills["alpha"] {
		t.Fatalf("manifest alpha skill_provenance digest: got %#v want %q", alpha["digest"], first.Manifest.Skills["alpha"])
	}
	if alpha["ref"] != "path:alpha" {
		t.Fatalf("manifest alpha skill_provenance ref: got %#v", alpha["ref"])
	}
	if _, ok := alpha["origin"]; ok {
		t.Fatalf("manifest should not contain skill origin: %#v", alpha)
	}
}

func TestBuildApplyPackageWithSkillRefsOmitsSkillFiles(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-ref-only", "alpha")
	writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse alpha.",
	})
	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}

	pkg, err := BuildApplyPackageWithSkillRefs(compiled, map[string]string{
		"alpha": "@user-abc/alpha:0.1.0",
	})
	if err != nil {
		t.Fatalf("BuildApplyPackageWithSkillRefs: %v", err)
	}

	entries := tarEntries(t, pkg.Bytes)
	if _, ok := entries["skills/alpha/SKILL.md"]; ok {
		t.Fatalf("registry-backed package should not vendor skill files: %v", sortedEntryNames(entries))
	}
	if _, ok := entries["SPEC.md"]; !ok {
		t.Fatalf("missing SPEC.md: %v", sortedEntryNames(entries))
	}
	if _, ok := entries["manifest.json"]; !ok {
		t.Fatalf("missing manifest.json: %v", sortedEntryNames(entries))
	}
	if pkg.Manifest.SkillProvenance["alpha"].Ref != "@user-abc/alpha:0.1.0" {
		t.Fatalf("skill provenance ref: %#v", pkg.Manifest.SkillProvenance["alpha"])
	}
	if _, err := ExtractApplyPackage(pkg.Bytes, t.TempDir()); err == nil {
		t.Fatal("ref-only package should require hydration before extraction")
	}
}

func TestHydrateApplyPackageFetchesReferencedSkills(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-hydrate", "alpha")
	writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md":        "---\nname: alpha\n---\nUse alpha.",
		"bin/tool.sh":     "#!/bin/sh\nexit 0\n",
		"reference/a.txt": "alpha",
	})
	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	bundleDigest, skillBundle, err := BuildSkillBundle(compiled.Skills[0])
	if err != nil {
		t.Fatalf("BuildSkillBundle: %v", err)
	}
	pkg, err := BuildApplyPackageWithSkillRefs(compiled, map[string]string{
		"alpha": "@user-abc/alpha:0.1.0",
	})
	if err != nil {
		t.Fatalf("BuildApplyPackageWithSkillRefs: %v", err)
	}
	if bundleDigest != pkg.Manifest.Skills["alpha"] {
		t.Fatalf("skill digest: bundle %s package %s", bundleDigest, pkg.Manifest.Skills["alpha"])
	}

	hydrated, manifest, err := HydrateApplyPackage(pkg.Bytes, func(req ApplyPackageSkillFetchRequest) ([]byte, error) {
		if req.Name != "alpha" || req.Ref != "@user-abc/alpha:0.1.0" || req.Digest != bundleDigest {
			t.Fatalf("fetch request: %#v", req)
		}
		return skillBundle, nil
	})
	if err != nil {
		t.Fatalf("HydrateApplyPackage: %v", err)
	}
	if manifest.Skills["alpha"] != bundleDigest {
		t.Fatalf("manifest skill digest: %#v", manifest.Skills)
	}
	dest := t.TempDir()
	if _, err := ExtractApplyPackage(hydrated, dest); err != nil {
		t.Fatalf("ExtractApplyPackage hydrated: %v", err)
	}
	if _, err := CompileEnvironmentWithBase(filepath.Join(dest, "SPEC.md"), dest); err != nil {
		t.Fatalf("CompileEnvironmentWithBase hydrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("hydrated skill file: %v", err)
	}
}

func TestBuildApplyPackageDigestChangesWhenSkillChanges(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-skill-change", "alpha")
	skillPath := writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse alpha.",
	})

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	first, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage first: %v", err)
	}

	if err := os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte("---\nname: alpha\n---\nUse changed alpha."), 0o644); err != nil {
		t.Fatalf("write changed skill: %v", err)
	}
	changed, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment changed: %v", err)
	}
	second, err := BuildApplyPackage(changed)
	if err != nil {
		t.Fatalf("BuildApplyPackage second: %v", err)
	}

	if first.Digest == second.Digest {
		t.Fatalf("digest did not change after skill content changed: %s", first.Digest)
	}
}

func TestBuildApplyPackageDigestIgnoresSkillFileCreationOrder(t *testing.T) {
	firstDir := t.TempDir()
	firstSpec := writePackageTestSpec(t, firstDir, "package-order", "alpha")
	writePackageTestSkill(t, firstDir, "alpha", map[string]string{
		"SKILL.md": "alpha",
		"b.txt":    "b",
		"a.txt":    "a",
	})

	secondDir := t.TempDir()
	secondSpec := writePackageTestSpec(t, secondDir, "package-order", "alpha")
	writePackageTestSkill(t, secondDir, "alpha", map[string]string{
		"a.txt":    "a",
		"b.txt":    "b",
		"SKILL.md": "alpha",
	})

	firstCompiled, err := CompileEnvironment(firstSpec)
	if err != nil {
		t.Fatalf("CompileEnvironment first: %v", err)
	}
	secondCompiled, err := CompileEnvironment(secondSpec)
	if err != nil {
		t.Fatalf("CompileEnvironment second: %v", err)
	}

	first, err := BuildApplyPackage(firstCompiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage first: %v", err)
	}
	second, err := BuildApplyPackage(secondCompiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage second: %v", err)
	}

	if first.Digest != second.Digest {
		t.Fatalf("digest depends on file creation order: %s != %s", first.Digest, second.Digest)
	}
}

func TestBuildApplyPackageNormalizesSkillFileModes(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-modes", "alpha")
	skillPath := writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md":        "---\nname: alpha\n---\nUse alpha.",
		"bin/tool.sh":     "#!/bin/sh\nexit 0\n",
		"reference/a.txt": "alpha",
	})
	if err := os.Chmod(filepath.Join(skillPath, "SKILL.md"), 0o664); err != nil {
		t.Fatalf("chmod SKILL.md: %v", err)
	}
	if err := os.Chmod(filepath.Join(skillPath, "bin", "tool.sh"), 0o775); err != nil {
		t.Fatalf("chmod tool.sh: %v", err)
	}

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	pkg, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage: %v", err)
	}

	modes := tarEntryModes(t, pkg.Bytes)
	for path, want := range map[string]int64{
		"skills/alpha/SKILL.md":        0o644,
		"skills/alpha/bin/tool.sh":     0o755,
		"skills/alpha/reference/a.txt": 0o644,
	} {
		if got := modes[path]; got != want {
			t.Fatalf("%s mode: got %04o, want %04o", path, got, want)
		}
	}

	dest := t.TempDir()
	if _, err := ExtractApplyPackage(pkg.Bytes, dest); err != nil {
		t.Fatalf("ExtractApplyPackage: %v", err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(dest, "skills", "alpha", "SKILL.md"):           0o644,
		filepath.Join(dest, "skills", "alpha", "bin", "tool.sh"):     0o755,
		filepath.Join(dest, "skills", "alpha", "reference", "a.txt"): 0o644,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s extracted mode: got %04o, want %04o", path, got, want)
		}
	}
}

func TestExtractApplyPackageCompilesWithPackageLocalSkills(t *testing.T) {
	srcDir := t.TempDir()
	specPath := writePackageTestSpec(t, srcDir, "package-local-skill", "alpha")
	writePackageTestSkill(t, srcDir, "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse package-local alpha.",
	})
	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	pkg, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage: %v", err)
	}

	dest := t.TempDir()
	manifest, err := ExtractApplyPackage(pkg.Bytes, dest)
	if err != nil {
		t.Fatalf("ExtractApplyPackage: %v", err)
	}
	if manifest.Spec.Digest != pkg.Manifest.Spec.Digest {
		t.Fatalf("spec digest: got %q want %q", manifest.Spec.Digest, pkg.Manifest.Spec.Digest)
	}
	extracted, err := CompileEnvironmentWithBase(filepath.Join(dest, "SPEC.md"), dest)
	if err != nil {
		t.Fatalf("CompileEnvironmentWithBase extracted: %v", err)
	}
	var found bool
	for _, skill := range extracted.Skills {
		if skill.Name == "alpha" {
			found = true
			if filepath.Dir(skill.Path) != filepath.Join(dest, "skills") {
				t.Fatalf("alpha resolved outside package: %s", skill.Path)
			}
		}
	}
	if !found {
		t.Fatal("missing extracted alpha skill")
	}
}

func TestCompilePackageLocalScopedSkillRef(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: 0.1.0\nname: scoped-package-skill\nplatform: cloud\nskills:\n  - '@telos/alpha:1.0.0'\n---\nUse alpha.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writePackageTestSkill(t, filepath.Join(dir, "skills"), "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse packaged alpha.",
	})
	manifest := ApplyPackageManifest{
		SchemaVersion: ApplyPackageSchemaVersion,
		Spec:          ApplyPackageSpecEntry{Digest: "sha256:spec"},
		Skills: map[string]string{
			"alpha": "sha256:skill",
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	compiled, err := CompileEnvironmentWithBase(specPath, dir)
	if err != nil {
		t.Fatalf("CompileEnvironmentWithBase: %v", err)
	}

	var found bool
	for _, skill := range compiled.Skills {
		if skill.Name == "alpha" {
			found = true
			if skill.Path != filepath.Join(dir, "skills", "alpha") {
				t.Fatalf("alpha resolved outside package: %s", skill.Path)
			}
		}
	}
	if !found {
		t.Fatal("missing package-local scoped alpha skill")
	}
}

func TestCompileUsesPackageManifestInjectedRequiredSkill(t *testing.T) {
	dir := t.TempDir()
	defaultSkills := filepath.Join(dir, "default-skills")
	writePackageTestSkill(t, defaultSkills, "verify-engineering", map[string]string{
		"SKILL.md": "---\nname: verify-engineering\n---\nDo not inject from catalogue.",
	})
	t.Setenv("TELOS_SKILLS_DIR", defaultSkills)
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: 0.1.0\nname: manifest-default-skill\nplatform: cloud\n---\nUse injected defaults.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writePackageTestSkill(t, filepath.Join(dir, "skills"), "verify-quality", map[string]string{
		"SKILL.md": "---\nname: verify-quality\n---\nVerify quality.",
	})
	manifest := ApplyPackageManifest{
		SchemaVersion: ApplyPackageSchemaVersion,
		Spec:          ApplyPackageSpecEntry{Digest: "sha256:spec"},
		Skills: map[string]string{
			"verify-quality": "sha256:skill",
		},
		SkillProvenance: map[string]ApplyPackageSkillProvenance{
			"verify-quality": {
				Ref:    "@telos/verify-quality:1.0.0",
				Digest: "sha256:skill",
			},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	compiled, err := CompileEnvironmentWithBase(specPath, dir)
	if err != nil {
		t.Fatalf("CompileEnvironmentWithBase: %v", err)
	}

	var found bool
	for _, skill := range compiled.Skills {
		if skill.Name == "verify-engineering" {
			t.Fatalf("package manifest compile loaded default catalogue skill: %s", skill.Path)
		}
		if skill.Name == "verify-quality" {
			found = true
			if skill.Path != filepath.Join(dir, "skills", "verify-quality") {
				t.Fatalf("verify-quality resolved outside package: %s", skill.Path)
			}
		}
	}
	if !found {
		t.Fatal("missing manifest-injected verify-quality skill")
	}
	if len(compiled.RequiredVerifierSkills) != 0 {
		t.Fatalf("package manifest should not mark required verifier skills: got %#v", compiled.RequiredVerifierSkills)
	}
}

func TestExtractApplyPackageRejectsUnmanifestedFiles(t *testing.T) {
	pkg := buildPackageTestPackage(t, "package-extra-file")
	entries := tarEntries(t, pkg.Bytes)
	files := packageFilesFromEntries(entries)
	files = append(files, packageFile{
		path: "skills/beta/SKILL.md",
		mode: 0o644,
		data: []byte("---\nname: beta\n---\nUnexpected beta.\n"),
	})
	data, err := writePackageTar(files)
	if err != nil {
		t.Fatalf("writePackageTar: %v", err)
	}

	if _, err := ExtractApplyPackage(data, t.TempDir()); err == nil {
		t.Fatal("expected unmanifested package file to be rejected")
	}
}

func TestExtractApplyPackageRejectsSkillDigestMismatch(t *testing.T) {
	pkg := buildPackageTestPackage(t, "package-skill-tamper")
	entries := tarEntries(t, pkg.Bytes)
	entries["skills/alpha/SKILL.md"] = []byte("---\nname: alpha\n---\nTampered alpha.\n")
	data, err := writePackageTar(packageFilesFromEntries(entries))
	if err != nil {
		t.Fatalf("writePackageTar: %v", err)
	}

	if _, err := ExtractApplyPackage(data, t.TempDir()); err == nil {
		t.Fatal("expected skill digest mismatch to be rejected")
	}
}

func TestExtractApplyPackageRejectsDuplicateEntries(t *testing.T) {
	pkg := buildPackageTestPackage(t, "package-duplicate-entry")
	files := packageFilesFromEntries(tarEntries(t, pkg.Bytes))
	files = append(files, packageFile{
		path: "SPEC.md",
		mode: 0o644,
		data: []byte("duplicate"),
	})
	data, err := writePackageTarPreservingOrder(files)
	if err != nil {
		t.Fatalf("writePackageTarPreservingOrder: %v", err)
	}

	if _, err := ExtractApplyPackage(data, t.TempDir()); err == nil {
		t.Fatal("expected duplicate package entry to be rejected")
	}
}

func writePackageTarPreservingOrder(files []packageFile) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, file := range files {
		header := &tar.Header{
			Name:     filepath.ToSlash(file.path),
			Mode:     file.mode,
			Size:     int64(len(file.data)),
			Typeflag: tar.TypeReg,
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

func buildPackageTestPackage(t *testing.T, name string) *ApplyPackage {
	t.Helper()
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, name, "alpha")
	writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse alpha.",
	})
	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	pkg, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage: %v", err)
	}
	return pkg
}

func packageFilesFromEntries(entries map[string][]byte) []packageFile {
	files := make([]packageFile, 0, len(entries))
	for path, data := range entries {
		files = append(files, packageFile{
			path: path,
			mode: 0o644,
			data: data,
		})
	}
	return files
}

func writePackageTestSpec(t *testing.T, dir, name, skill string) string {
	t.Helper()
	path := filepath.Join(dir, "SPEC.md")
	data := []byte("---\nversion: 0.1.0\nname: " + name + "\nplatform: cloud\nskills:\n  - " + skill + "\n---\nBuild the package.\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}

func writePackageTestSkill(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(dir, name)
	for rel, data := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create skill dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("write skill file: %v", err)
		}
	}
	return root
}

func tarEntries(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := map[string][]byte{}
	for {
		header, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("read tar: %v", err)
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(tr); err != nil {
			t.Fatalf("read tar entry %s: %v", header.Name, err)
		}
		entries[header.Name] = buf.Bytes()
	}
	return entries
}

func sortedEntryNames(entries map[string][]byte) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func tarEntryModes(t *testing.T, data []byte) map[string]int64 {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	modes := map[string]int64{}
	for {
		header, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("read tar: %v", err)
		}
		modes[header.Name] = header.Mode
	}
	return modes
}
