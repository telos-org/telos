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
	if got := digestPackage(pkg.Manifest.Spec.Digest, pkg.Manifest.Skills); got != pkg.Digest {
		t.Fatalf("digest not deterministic for identical locks: %s != %s", got, pkg.Digest)
	}
	flipped := make(map[string]ApplyPackageSkillLock, len(pkg.Manifest.Skills))
	for name, lock := range pkg.Manifest.Skills {
		lock.Starred = !lock.Starred
		flipped[name] = lock
	}
	if got := digestPackage(pkg.Manifest.Spec.Digest, flipped); got == pkg.Digest {
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
		SchemaVersion: ApplyPackageSchemaVersion,
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
		SchemaVersion: ApplyPackageSchemaVersion,
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
