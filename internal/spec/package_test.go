package spec

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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

	first, err := BuildApplyPackage(compiled, ApplyPackageOptions{CompilerVersion: "test-compiler"})
	if err != nil {
		t.Fatalf("BuildApplyPackage first: %v", err)
	}
	second, err := BuildApplyPackage(compiled, ApplyPackageOptions{CompilerVersion: "test-compiler"})
	if err != nil {
		t.Fatalf("BuildApplyPackage second: %v", err)
	}

	if first.Digest != second.Digest {
		t.Fatalf("digest changed: %s != %s", first.Digest, second.Digest)
	}
	if !bytes.Equal(first.Bytes, second.Bytes) {
		t.Fatal("package bytes changed for identical inputs")
	}
	if first.Lock.PackageDigest != first.Digest {
		t.Fatalf("lock digest %q != package digest %q", first.Lock.PackageDigest, first.Digest)
	}
	if first.Lock.RootSpecPath != "SPEC.md" {
		t.Fatalf("root spec path = %q, want SPEC.md", first.Lock.RootSpecPath)
	}

	entries := tarEntries(t, first.Bytes)
	for _, want := range []string{
		"manifest-lock.yaml",
		"SPEC.md",
		"skills/alpha/SKILL.md",
		"skills/alpha/reference/example.txt",
	} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("missing package entry %q; entries=%v", want, sortedEntryNames(entries))
		}
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
	first, err := BuildApplyPackage(compiled, ApplyPackageOptions{CompilerVersion: "test-compiler"})
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
	second, err := BuildApplyPackage(changed, ApplyPackageOptions{CompilerVersion: "test-compiler"})
	if err != nil {
		t.Fatalf("BuildApplyPackage second: %v", err)
	}

	if first.Digest == second.Digest {
		t.Fatalf("digest did not change after skill content changed: %s", first.Digest)
	}
}

func TestBuildApplyPackageDigestChangesWhenRuntimeVersionChanges(t *testing.T) {
	dir := t.TempDir()
	specPath := writePackageTestSpec(t, dir, "package-runtime-change", "alpha")
	writePackageTestSkill(t, dir, "alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\n---\nUse alpha.",
	})
	compiled, err := CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}

	first, err := BuildApplyPackage(compiled, ApplyPackageOptions{CompilerVersion: "test-compiler", RuntimeVersion: "v1"})
	if err != nil {
		t.Fatalf("BuildApplyPackage first: %v", err)
	}
	second, err := BuildApplyPackage(compiled, ApplyPackageOptions{CompilerVersion: "test-compiler", RuntimeVersion: "v2"})
	if err != nil {
		t.Fatalf("BuildApplyPackage second: %v", err)
	}

	if first.Digest == second.Digest {
		t.Fatalf("digest did not change after runtime version changed: %s", first.Digest)
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

	first, err := BuildApplyPackage(firstCompiled, ApplyPackageOptions{CompilerVersion: "test-compiler"})
	if err != nil {
		t.Fatalf("BuildApplyPackage first: %v", err)
	}
	second, err := BuildApplyPackage(secondCompiled, ApplyPackageOptions{CompilerVersion: "test-compiler"})
	if err != nil {
		t.Fatalf("BuildApplyPackage second: %v", err)
	}

	if first.Digest != second.Digest {
		t.Fatalf("digest depends on file creation order: %s != %s", first.Digest, second.Digest)
	}
}

func writePackageTestSpec(t *testing.T, dir, name, skill string) string {
	t.Helper()
	path := filepath.Join(dir, "SPEC.md")
	data := []byte("---\nversion: v0\nname: " + name + "\nplatform: cloud\nskills:\n  - " + skill + "\n---\nBuild the package.\n")
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
