package telosd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

func TestApplyPackageMaterializerFetchesVerifiesAndCaches(t *testing.T) {
	pkg := buildMaterializerTestPackage(t, "postgres")
	root := t.TempDir()
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if got := r.Header.Get("Authorization"); got != "Bearer runtime-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/"+pkg.Digest+"/bundle") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write(pkg.Bytes)
	}))
	defer server.Close()
	t.Setenv("TELOS_PACKAGE_BUNDLE_BASE_URL", server.URL)

	materializer := newApplyPackageMaterializer(root, "runtime-token")
	materializer.client = server.Client()

	path, err := materializer.Ensure(context.Background(), pkg.Digest)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := sessionapi.VerifyPackageDigest(path, pkg.Digest); err != nil {
		t.Fatalf("VerifyPackageDigest: %v", err)
	}

	if _, err := materializer.Ensure(context.Background(), pkg.Digest); err != nil {
		t.Fatalf("Ensure cache hit: %v", err)
	}
	if hits != 1 {
		t.Fatalf("server hits = %d want 1", hits)
	}
}

func TestApplyPackageMaterializerHydratesReferencedSkills(t *testing.T) {
	pkg, skillDigest, skillBundle := buildMaterializerRefOnlyPackage(t, "postgres")
	root := t.TempDir()
	packageHits := 0
	skillHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer runtime-token" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case strings.Contains(r.URL.Path, "/packages/blobs/"):
			packageHits++
			if !strings.HasSuffix(r.URL.Path, "/"+pkg.Digest+"/bundle") {
				t.Fatalf("package path = %q", r.URL.Path)
			}
			_, _ = w.Write(pkg.Bytes)
		case strings.Contains(r.URL.Path, "/skills/blobs/"):
			skillHits++
			if !strings.HasSuffix(r.URL.Path, "/"+skillDigest+"/bundle") {
				t.Fatalf("skill path = %q", r.URL.Path)
			}
			_, _ = w.Write(skillBundle)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("TELOS_PACKAGE_BUNDLE_BASE_URL", server.URL+"/api/packages/blobs")

	materializer := newApplyPackageMaterializer(root, "runtime-token")
	materializer.client = server.Client()

	path, err := materializer.Ensure(context.Background(), pkg.Digest)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := sessionapi.VerifyPackageDigest(path, pkg.Digest); err != nil {
		t.Fatalf("VerifyPackageDigest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skill-blobs", "sha256", strings.TrimPrefix(skillDigest, "sha256:"), "skill.tar.gz")); err != nil {
		t.Fatalf("cached skill bundle: %v", err)
	}
	if _, err := materializer.Ensure(context.Background(), pkg.Digest); err != nil {
		t.Fatalf("Ensure cache hit: %v", err)
	}
	if packageHits != 1 || skillHits != 1 {
		t.Fatalf("hits: package=%d skill=%d", packageHits, skillHits)
	}
}

func TestApplyPackageMaterializerRejectsWrongDigest(t *testing.T) {
	pkg := buildMaterializerTestPackage(t, "postgres")
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not a package"))
	}))
	defer server.Close()
	t.Setenv("TELOS_PACKAGE_BUNDLE_BASE_URL", server.URL)

	materializer := newApplyPackageMaterializer(root, "runtime-token")
	materializer.client = server.Client()

	if _, err := materializer.Ensure(context.Background(), pkg.Digest); err == nil {
		t.Fatal("expected digest verification failure")
	}
	path, err := sessionapi.PackagePathForDigest(root, pkg.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("package cache entry should not exist after failed verify: %v", err)
	}
}

func buildMaterializerRefOnlyPackage(t *testing.T, name string) (*spec.ApplyPackage, string, []byte) {
	t.Helper()
	compiled := buildMaterializerTestCompiled(t, name)
	skillDigest, skillBundle, err := spec.BuildSkillBundle(compiled.Skills[0])
	if err != nil {
		t.Fatalf("BuildSkillBundle: %v", err)
	}
	pkg, err := spec.BuildApplyPackageWithSkillRefs(compiled, map[string]string{
		"alpha": "@user-test/alpha:0.1.0",
	})
	if err != nil {
		t.Fatalf("BuildApplyPackageWithSkillRefs: %v", err)
	}
	if pkg.Manifest.Skills["alpha"].Digest != skillDigest {
		t.Fatalf("package skill digest = %s want %s", pkg.Manifest.Skills["alpha"].Digest, skillDigest)
	}
	return pkg, skillDigest, skillBundle
}

func buildMaterializerTestPackage(t *testing.T, name string, versions ...string) *spec.ApplyPackage {
	t.Helper()
	compiled := buildMaterializerTestCompiled(t, name, versions...)
	pkg, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		t.Fatalf("BuildApplyPackage: %v", err)
	}
	return pkg
}

func buildMaterializerTestCompiled(t *testing.T, name string, versions ...string) *spec.CompiledEnvironment {
	t.Helper()
	version := "0.1.0"
	if len(versions) > 0 && versions[0] != "" {
		version = versions[0]
	}
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: "+version+"\nname: "+name+"\nplatform: cloud\nskills:\n  - alpha\n---\n# "+name+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "skills", "alpha")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: alpha\n---\nUse alpha.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	return compiled
}

func writePackageCacheEntry(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
