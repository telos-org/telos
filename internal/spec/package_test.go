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
	"strings"
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
	if first.Manifest.Skills["alpha"].Digest == "" {
		t.Fatalf("manifest missing alpha skill digest: %#v", first.Manifest.Skills)
	}
	if first.Manifest.Skills["alpha"].Ref != "path:alpha" {
		t.Fatalf("manifest alpha skill ref: got %q", first.Manifest.Skills["alpha"].Ref)
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
	if _, ok := manifest["skill_provenance"]; ok {
		t.Fatalf("manifest should not contain skill_provenance: %#v", manifest)
	}
	rawSkills, ok := manifest["skills"].(map[string]any)
	if !ok {
		t.Fatalf("manifest missing skills: %#v", manifest)
	}
	alpha, ok := rawSkills["alpha"].(map[string]any)
	if !ok {
		t.Fatalf("manifest missing alpha skill lock: %#v", rawSkills)
	}
	if alpha["digest"] != first.Manifest.Skills["alpha"].Digest {
		t.Fatalf("manifest alpha skill digest: got %#v want %q", alpha["digest"], first.Manifest.Skills["alpha"].Digest)
	}
	if alpha["ref"] != "path:alpha" {
		t.Fatalf("manifest alpha skill ref: got %#v", alpha["ref"])
	}
	if _, ok := alpha["origin"]; ok {
		t.Fatalf("manifest should not contain skill origin: %#v", alpha)
	}
}

func TestApplyPackageV3GoldenVector(t *testing.T) {
	data, err := os.ReadFile("testdata/apply-package-v3-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		SchemaVersion  int                              `json:"schema_version"`
		Spec           string                           `json:"spec"`
		SpecDigest     string                           `json:"spec_digest"`
		Skills         map[string]ApplyPackageSkillLock `json:"skills"`
		ExpectedDigest string                           `json:"expected_digest"`
	}
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	if got := digestBytes([]byte(vector.Spec)); got != vector.SpecDigest {
		t.Fatalf("spec digest: got %s want %s", got, vector.SpecDigest)
	}
	manifest := ApplyPackageManifest{
		SchemaVersion: vector.SchemaVersion,
		Spec:          ApplyPackageSpecEntry{Digest: vector.SpecDigest},
		Skills:        vector.Skills,
	}
	if got := ApplyPackageDigest(&manifest); got != vector.ExpectedDigest {
		t.Fatalf("package digest: got %s want %s", got, vector.ExpectedDigest)
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := writePackageTar([]packageFile{
		{path: "SPEC.md", mode: 0o644, data: []byte(vector.Spec)},
		{path: "manifest.json", mode: 0o644, data: manifestData},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, parsed, err := ApplyPackageSpec(bundle); err != nil {
		t.Fatalf("read golden V3 package: %v", err)
	} else if got := ApplyPackageDigest(parsed); got != vector.ExpectedDigest {
		t.Fatalf("parsed package digest: got %s want %s", got, vector.ExpectedDigest)
	}

	changed := manifest
	changed.Skills = map[string]ApplyPackageSkillLock{}
	for name, lock := range manifest.Skills {
		changed.Skills[name] = lock
	}
	lock := changed.Skills["lint"]
	lock.Ref = "@bob/lint:1.2.3"
	changed.Skills["lint"] = lock
	if ApplyPackageDigest(&changed) == vector.ExpectedDigest {
		t.Fatal("changing an exact skill ref must change the V3 package digest")
	}
}

func TestApplyPackageManifestV3RejectsUnknownFieldsRecursively(t *testing.T) {
	for name, raw := range map[string]string{
		"manifest": `{"schema_version":3,"spec":{"digest":"sha256:x"},"skills":{},"future":true}`,
		"spec":     `{"schema_version":3,"spec":{"digest":"sha256:x","future":true},"skills":{}}`,
		"lock":     `{"schema_version":3,"spec":{"digest":"sha256:x"},"skills":{"alpha":{"digest":"sha256:y","ref":"@alice/alpha:1.0.0","future":true}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var manifest ApplyPackageManifest
			if err := json.Unmarshal([]byte(raw), &manifest); err == nil {
				t.Fatal("expected V3 manifest with an unknown field to be rejected")
			}
		})
	}
}

func TestApplyPackageManifestLegacyToleratesUnknownFields(t *testing.T) {
	raw := `{"schema_version":1,"spec":{"digest":"sha256:x","compiler":"legacy"},"skills":{"alpha":{"digest":"sha256:y","legacy":true}},"runtime":"v1"}`
	var manifest ApplyPackageManifest
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		t.Fatalf("legacy manifest compatibility: %v", err)
	}
	if manifest.SchemaVersion != ApplyPackageSchemaVersion {
		t.Fatalf("schema version: got %d", manifest.SchemaVersion)
	}
	if manifest.Skills["alpha"].Digest != "sha256:y" {
		t.Fatalf("skill lock: %#v", manifest.Skills["alpha"])
	}
}

func TestBuildApplyPackageWithSkillRefsUsesV3AndRequiresCanonicalExactRefs(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-v3-refs", "alpha")
	writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse alpha.",
	})
	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := BuildApplyPackageWithSkillRefs(compiled, map[string]string{
		"alpha": "@alice/alpha:1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.SchemaVersion != ApplyPackageSchemaVersionExactRefs {
		t.Fatalf("schema version: got %d want %d", pkg.Manifest.SchemaVersion, ApplyPackageSchemaVersionExactRefs)
	}
	for _, invalid := range []string{
		"@alice/alpha",
		" @alice/alpha:1.0.0",
		"@Alice/alpha:1.0.0",
		"skill:@alice/alpha:1.0.0",
		"@alice/different:1.0.0",
	} {
		if _, err := BuildApplyPackageWithSkillRefs(compiled, map[string]string{"alpha": invalid}); err == nil {
			t.Fatalf("expected non-canonical ref %q to fail", invalid)
		}
	}
	if _, err := BuildApplyPackageWithSkillRefs(compiled, map[string]string{}); err == nil {
		t.Fatal("expected a missing exact ref to fail")
	}
}

func TestApplyPackageV3RegistryReaderRejectsEmbeddedSkillFiles(t *testing.T) {
	specData := []byte("---\nname: package-v3-embedded\nversion: 1.0.0\nplatform: cloud\n---\nTest.\n")
	skillData := []byte("---\nname: alpha\n---\nUse alpha.\n")
	entry := ApplyPackageFileEntry{
		Path:   "SKILL.md",
		Mode:   "0644",
		Digest: digestFile("SKILL.md", 0o644, skillData),
	}
	manifest := ApplyPackageManifest{
		SchemaVersion: ApplyPackageSchemaVersionExactRefs,
		Spec:          ApplyPackageSpecEntry{Digest: digestBytes(specData)},
		Skills: map[string]ApplyPackageSkillLock{
			"alpha": {
				Digest: digestSkill("alpha", []ApplyPackageFileEntry{entry}),
				Ref:    "@alice/alpha:1.0.0",
			},
		},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := writePackageTar([]packageFile{
		{path: "SPEC.md", mode: 0o644, data: specData},
		{path: "manifest.json", mode: 0o644, data: manifestData},
		{path: "skills/alpha/SKILL.md", mode: 0o644, data: skillData},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplyPackageSpec(bundle); err == nil || !strings.Contains(err.Error(), "must not embed") {
		t.Fatalf("expected embedded V3 skill rejection, got %v", err)
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
	if pkg.Manifest.Skills["alpha"].Ref != "@user-abc/alpha:0.1.0" {
		t.Fatalf("skill ref: %#v", pkg.Manifest.Skills["alpha"])
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
	if bundleDigest != pkg.Manifest.Skills["alpha"].Digest {
		t.Fatalf("skill digest: bundle %s package %s", bundleDigest, pkg.Manifest.Skills["alpha"].Digest)
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
	if manifest.Skills["alpha"].Digest != bundleDigest {
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

func TestHydrateApplyPackageEnforcesAggregateSkillLimits(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-hydrate-limits", "alpha")
	writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md":    "---\nname: alpha\n---\nUse alpha.",
		"reference/a": "alpha",
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
	packageFiles, _, err := readApplyPackage(pkg.Bytes)
	if err != nil {
		t.Fatalf("readApplyPackage: %v", err)
	}
	skillFiles, err := readSkillBundleFiles("alpha", skillBundle)
	if err != nil {
		t.Fatalf("readSkillBundleFiles: %v", err)
	}
	packageBytes := packageFileBytes(packageFiles)
	skillBytes := int64(0)
	for _, file := range skillFiles {
		skillBytes += int64(len(file.data))
	}
	fetch := func(ApplyPackageSkillFetchRequest) ([]byte, error) {
		return skillBundle, nil
	}

	fileLimits := defaultArchiveReadLimits()
	fileLimits.maxFiles = len(packageFiles) + len(skillFiles) - 1
	if _, _, err := hydrateApplyPackageWithLimits(pkg.Bytes, fetch, fileLimits); err == nil || !strings.Contains(err.Error(), "contains more than") {
		t.Fatalf("expected aggregate file-count rejection, got %v", err)
	}

	byteLimits := defaultArchiveReadLimits()
	byteLimits.maxExpandedBytes = packageBytes + skillBytes - 1
	if _, _, err := hydrateApplyPackageWithLimits(pkg.Bytes, fetch, byteLimits); err == nil || !strings.Contains(err.Error(), "expanded content exceeds") {
		t.Fatalf("expected aggregate expanded-size rejection, got %v", err)
	}
	if pkg.Manifest.Skills["alpha"].Digest != bundleDigest {
		t.Fatalf("skill digest: got %s want %s", pkg.Manifest.Skills["alpha"].Digest, bundleDigest)
	}
}

func TestHydrateApplyPackageFetchesLegacyReferencedSkills(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-legacy-hydrate", "alpha")
	writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse alpha.",
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
	entries := tarEntries(t, pkg.Bytes)
	legacySkills := map[string]string{}
	legacyProvenance := map[string]map[string]string{}
	for name, lock := range pkg.Manifest.Skills {
		legacySkills[name] = lock.Digest
		if lock.Ref != "" {
			legacyProvenance[name] = map[string]string{
				"digest": lock.Digest,
				"ref":    lock.Ref,
			}
		}
	}
	legacyManifest := map[string]any{
		"schema_version":   ApplyPackageSchemaVersion,
		"spec":             map[string]string{"digest": pkg.Manifest.Spec.Digest},
		"skills":           legacySkills,
		"skill_provenance": legacyProvenance,
	}
	manifestData, err := json.Marshal(legacyManifest)
	if err != nil {
		t.Fatalf("marshal legacy manifest: %v", err)
	}
	entries["manifest.json"] = manifestData
	legacyPackage, err := writePackageTar(packageFilesFromEntries(entries))
	if err != nil {
		t.Fatalf("write legacy package: %v", err)
	}

	hydrated, manifest, err := HydrateApplyPackage(legacyPackage, func(req ApplyPackageSkillFetchRequest) ([]byte, error) {
		if req.Name != "alpha" || req.Ref != "@user-abc/alpha:0.1.0" || req.Digest != bundleDigest {
			t.Fatalf("fetch request: %#v", req)
		}
		return skillBundle, nil
	})
	if err != nil {
		t.Fatalf("HydrateApplyPackage: %v", err)
	}
	if manifest.Skills["alpha"].Digest != bundleDigest {
		t.Fatalf("manifest skill digest: %#v", manifest.Skills)
	}
	if _, err := ExtractApplyPackage(hydrated, t.TempDir()); err != nil {
		t.Fatalf("ExtractApplyPackage hydrated: %v", err)
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

func TestApplyPackageSpecReturnsVerifiedRoot(t *testing.T) {
	root := t.TempDir()
	specPath := filepath.Join(root, "SPEC.md")
	if err := os.WriteFile(specPath, []byte(`---
name: demo
version: 1.0.0
platform: cloud
---

# Goal

Serve a demo.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatal(err)
	}
	markdown, manifest, err := ApplyPackageSpec(pkg.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "name: demo") {
		t.Fatalf("root spec: %s", markdown)
	}
	if got := ApplyPackageDigest(manifest); got != pkg.Digest {
		t.Fatalf("digest: got %s want %s", got, pkg.Digest)
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
		Skills: map[string]ApplyPackageSkillLock{
			"alpha": {Digest: "sha256:skill"},
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

func TestCompileUsesPackageManifestSkill(t *testing.T) {
	dir := t.TempDir()
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
		Skills: map[string]ApplyPackageSkillLock{
			"verify-quality": {
				Digest: "sha256:skill",
				Ref:    "@telos/verify-quality:1.0.0",
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
		t.Fatalf("unstarred package manifest locks should not mark required verifier skills: got %#v", compiled.RequiredVerifierSkills)
	}
}

func TestBuildApplyPackageMarksStarredSkillLocks(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	data := []byte("---\nversion: 0.1.0\nname: package-starred\nplatform: cloud\nskills:\n  - alpha*\n  - beta\n---\nBuild the package.\n")
	if err := os.WriteFile(specPath, data, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	writePackageTestSkill(t, filepath.Join(dir, "skills"), "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse alpha.",
	})
	writePackageTestSkill(t, filepath.Join(dir, "skills"), "beta", map[string]string{
		"SKILL.md": "---\nname: beta\n---\nUse beta.",
	})

	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	pkg, err := BuildApplyPackage(compiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage: %v", err)
	}

	if !pkg.Manifest.Skills["alpha"].Starred {
		t.Fatalf("starred skill lock not marked: %#v", pkg.Manifest.Skills["alpha"])
	}
	if pkg.Manifest.Skills["beta"].Starred {
		t.Fatalf("unstarred skill lock marked: %#v", pkg.Manifest.Skills["beta"])
	}
	// Starred packages declare the gated schema version so pre-starred
	// runtimes reject them explicitly instead of mis-deriving the digest.
	if pkg.Manifest.SchemaVersion != ApplyPackageSchemaVersionStarred {
		t.Fatalf("starred package schema_version: got %d", pkg.Manifest.SchemaVersion)
	}

	entries := tarEntries(t, pkg.Bytes)
	var manifest map[string]any
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	rawSkills := manifest["skills"].(map[string]any)
	alpha := rawSkills["alpha"].(map[string]any)
	if alpha["starred"] != true {
		t.Fatalf("manifest alpha lock missing starred: %#v", alpha)
	}
	beta := rawSkills["beta"].(map[string]any)
	if _, ok := beta["starred"]; ok {
		t.Fatalf("unstarred lock should omit starred key: %#v", beta)
	}

	// Starred changes runtime semantics, so it is part of package identity:
	// flipping a flag must change the digest, while identical locks digest
	// deterministically.
	if got := digestPackage(pkg.Manifest.SchemaVersion, pkg.Manifest.Spec.Digest, pkg.Manifest.Skills); got != pkg.Digest {
		t.Fatalf("digest not deterministic for identical locks: %s != %s", got, pkg.Digest)
	}
	flipped := make(map[string]ApplyPackageSkillLock, len(pkg.Manifest.Skills))
	for name, lock := range pkg.Manifest.Skills {
		lock.Starred = !lock.Starred
		flipped[name] = lock
	}
	if got := digestPackage(pkg.Manifest.SchemaVersion, pkg.Manifest.Spec.Digest, flipped); got == pkg.Digest {
		t.Fatalf("package digest must include the starred flag: %s", got)
	}

	// The push-time rebuild with registry refs keeps the marker.
	refPkg, err := BuildApplyPackageWithSkillRefs(compiled, map[string]string{
		"alpha": "@user-abc/alpha:0.1.0",
		"beta":  "@user-abc/beta:0.1.0",
	})
	if err != nil {
		t.Fatalf("BuildApplyPackageWithSkillRefs: %v", err)
	}
	if !refPkg.Manifest.Skills["alpha"].Starred || refPkg.Manifest.Skills["beta"].Starred {
		t.Fatalf("ref rebuild lost starred markers: %#v", refPkg.Manifest.Skills)
	}
}

func TestApplyPackageSkillLockUnmarshalStarred(t *testing.T) {
	var lock ApplyPackageSkillLock
	if err := json.Unmarshal([]byte(`{"digest":"sha256:abc","ref":"@a/b:1.0.0","starred":true}`), &lock); err != nil {
		t.Fatalf("unmarshal starred lock: %v", err)
	}
	if !lock.Starred || lock.Digest != "sha256:abc" || lock.Ref != "@a/b:1.0.0" {
		t.Fatalf("starred lock round-trip: %#v", lock)
	}
	if err := json.Unmarshal([]byte(`{"digest":"sha256:abc"}`), &lock); err != nil {
		t.Fatalf("unmarshal plain lock: %v", err)
	}
	if lock.Starred {
		t.Fatalf("plain lock should not be starred: %#v", lock)
	}
	lock.Starred = true
	if err := json.Unmarshal([]byte(`"sha256:abc"`), &lock); err != nil {
		t.Fatalf("unmarshal legacy lock: %v", err)
	}
	if lock.Starred {
		t.Fatalf("legacy string lock should reset starred: %#v", lock)
	}
}

func TestCompileHonorsPackageManifestStarredSkill(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: 0.1.0\nname: manifest-starred-skill\nplatform: cloud\n---\nUse injected skills.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writePackageTestSkill(t, filepath.Join(dir, "skills"), "verify-quality", map[string]string{
		"SKILL.md": "---\nname: verify-quality\n---\nVerify quality.",
	})
	manifest := ApplyPackageManifest{
		SchemaVersion: ApplyPackageSchemaVersionStarred,
		Spec:          ApplyPackageSpecEntry{Digest: "sha256:spec"},
		Skills: map[string]ApplyPackageSkillLock{
			"verify-quality": {
				Digest:  "sha256:skill",
				Ref:     "@telos/verify-quality:1.0.0",
				Starred: true,
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

	if len(compiled.RequiredVerifierSkills) != 1 || compiled.RequiredVerifierSkills[0].Name != "verify-quality" {
		t.Fatalf("starred manifest lock should mark required verifier skill: got %#v", compiled.RequiredVerifierSkills)
	}
}

func TestCompileSpecDeclarationOverridesManifestStar(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: 0.1.0\nname: spec-wins-star\nplatform: cloud\nskills:\n  - verify-quality\n---\nSpec declares it unstarred.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writePackageTestSkill(t, filepath.Join(dir, "skills"), "verify-quality", map[string]string{
		"SKILL.md": "---\nname: verify-quality\n---\nVerify quality.",
	})
	manifest := ApplyPackageManifest{
		SchemaVersion: ApplyPackageSchemaVersionStarred,
		Spec:          ApplyPackageSpecEntry{Digest: "sha256:spec"},
		Skills: map[string]ApplyPackageSkillLock{
			"verify-quality": {
				Digest:  "sha256:skill",
				Starred: true,
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

	if len(compiled.RequiredVerifierSkills) != 0 {
		t.Fatalf("spec-declared unstarred skill must override stale manifest star: got %#v", compiled.RequiredVerifierSkills)
	}
}

func TestCompileSpecDeclarationOverridesManifestStarByDeclaredName(t *testing.T) {
	// The spec declares the skill through a directory whose name differs from
	// the SKILL.md-declared name; the stale manifest star is keyed by the
	// declared name. The override must match on resolved names, not paths.
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: 0.1.0\nname: spec-wins-star-alias\nplatform: cloud\nskills:\n  - local-review\n---\nDeclared unstarred under an alias directory.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writePackageTestSkill(t, filepath.Join(dir, "skills"), "local-review", map[string]string{
		"SKILL.md": "---\nname: verify-security\n---\nReview security.",
	})
	writePackageTestSkill(t, filepath.Join(dir, "skills"), "verify-security", map[string]string{
		"SKILL.md": "---\nname: verify-security\n---\nReview security.",
	})
	manifest := ApplyPackageManifest{
		SchemaVersion: ApplyPackageSchemaVersionStarred,
		Spec:          ApplyPackageSpecEntry{Digest: "sha256:spec"},
		Skills: map[string]ApplyPackageSkillLock{
			"verify-security": {
				Digest:  "sha256:skill",
				Starred: true,
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

	if len(compiled.RequiredVerifierSkills) != 0 {
		t.Fatalf("spec declaration must override the manifest star by resolved name: got %#v", compiled.RequiredVerifierSkills)
	}
}

func TestExtractApplyPackageRejectsStarredLockInSchemaV1(t *testing.T) {
	// A schema-1 manifest carrying a starred lock never passes Cloud, but a
	// locally supplied package bypasses Cloud entirely — the shared validator
	// must reject the combination so a current runtime cannot accept a
	// package that older runtimes would mis-hash.
	pkg := buildPackageTestPackage(t, "package-v1-starred")
	entries := tarEntries(t, pkg.Bytes)
	var manifest map[string]any
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if manifest["schema_version"] != float64(ApplyPackageSchemaVersion) {
		t.Fatalf("fixture should start at schema 1: %#v", manifest["schema_version"])
	}
	skills := manifest["skills"].(map[string]any)
	alpha := skills["alpha"].(map[string]any)
	alpha["starred"] = true
	mutated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entries["manifest.json"] = mutated
	data, err := writePackageTar(packageFilesFromEntries(entries))
	if err != nil {
		t.Fatalf("writePackageTar: %v", err)
	}

	_, err = ExtractApplyPackage(data, t.TempDir())
	if err == nil {
		t.Fatal("expected schema-1 starred lock to be rejected")
	}
	if !strings.Contains(err.Error(), "starred skill locks require schema_version 2") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompileIgnoresStarredLockInSchemaV1(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: 0.1.0\nname: manifest-v1-starred\nplatform: cloud\n---\nUse injected skills.\n"),
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
		Skills: map[string]ApplyPackageSkillLock{
			"verify-quality": {
				Digest:  "sha256:skill",
				Starred: true,
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

	// The invalid schema-1 star is ignored, but the skill itself still loads.
	if len(compiled.RequiredVerifierSkills) != 0 {
		t.Fatalf("schema-1 starred lock must not mark required verifier skills: got %#v", compiled.RequiredVerifierSkills)
	}
	var found bool
	for _, skill := range compiled.Skills {
		if skill.Name == "verify-quality" {
			found = true
		}
	}
	if !found {
		t.Fatal("manifest-injected skill missing from compile")
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

func TestReadSkillBundleFilesEnforcesArchiveLimits(t *testing.T) {
	bundle, err := writePackageTarPreservingOrder([]packageFile{
		{path: "SKILL.md", mode: 0o644, data: []byte("skill")},
		{path: "reference/a", mode: 0o644, data: []byte("data")},
	})
	if err != nil {
		t.Fatalf("writePackageTarPreservingOrder: %v", err)
	}
	limits := archiveReadLimits{
		maxFiles:           2,
		maxExpandedBytes:   9,
		maxEntryBytes:      5,
		maxPathBytes:       64,
		maxArchiveBytes:    1024 * 1024,
		maxCompressedBytes: int64(len(bundle)),
	}
	files, err := readSkillBundleFilesWithLimits("alpha", bundle, limits)
	if err != nil {
		t.Fatalf("exact limits: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("exact limits file count: got %d want 2", len(files))
	}

	tests := []struct {
		name   string
		adjust func(*archiveReadLimits)
		want   string
	}{
		{
			name: "compressed body",
			adjust: func(limits *archiveReadLimits) {
				limits.maxCompressedBytes--
			},
			want: "compressed content exceeds",
		},
		{
			name: "file count",
			adjust: func(limits *archiveReadLimits) {
				limits.maxFiles--
			},
			want: "contains more than",
		},
		{
			name: "single entry",
			adjust: func(limits *archiveReadLimits) {
				limits.maxEntryBytes--
			},
			want: "entry \"SKILL.md\" exceeds",
		},
		{
			name: "expanded content",
			adjust: func(limits *archiveReadLimits) {
				limits.maxExpandedBytes--
			},
			want: "expanded content exceeds",
		},
		{
			name: "path",
			adjust: func(limits *archiveReadLimits) {
				limits.maxPathBytes = len("SKILL.md") - 1
			},
			want: "entry path exceeds",
		},
		{
			name: "archive body",
			adjust: func(limits *archiveReadLimits) {
				limits.maxArchiveBytes = 1
			},
			want: "read skill bundle",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limited := limits
			test.adjust(&limited)
			if _, err := readSkillBundleFilesWithLimits("alpha", bundle, limited); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestReadSkillBundleFilesRejectsDuplicateEntries(t *testing.T) {
	bundle, err := writePackageTarPreservingOrder([]packageFile{
		{path: "SKILL.md", mode: 0o644, data: []byte("first")},
		{path: "SKILL.md", mode: 0o644, data: []byte("second")},
	})
	if err != nil {
		t.Fatalf("writePackageTarPreservingOrder: %v", err)
	}
	if _, err := readSkillBundleFiles("alpha", bundle); err == nil || !strings.Contains(err.Error(), "duplicate skill bundle entry") {
		t.Fatalf("expected duplicate entry rejection, got %v", err)
	}
}

func packageFileBytes(files map[string]packageFile) int64 {
	total := int64(0)
	for _, file := range files {
		total += int64(len(file.data))
	}
	return total
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
