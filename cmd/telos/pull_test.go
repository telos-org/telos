package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func TestParsePackageReference(t *testing.T) {
	reference, err := parsePackageReference("@telos/bifrost:1.0.10")
	if err != nil {
		t.Fatal(err)
	}
	if reference.scope != "telos" ||
		reference.name != "bifrost" ||
		reference.version != "1.0.10" {
		t.Fatalf("reference: got %+v", reference)
	}
	for _, input := range []string{
		"telos/bifrost:1.0.10",
		"@telos/bifrost",
		"@telos/bifrost:latest",
		"@telos/Bad:1.0.10",
	} {
		if _, err := parsePackageReference(input); err == nil {
			t.Fatalf("parsePackageReference(%q) succeeded", input)
		}
	}
}

func TestPackageForSessionUsesAttachedVersion(t *testing.T) {
	pkg := testApplyPackage(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/deployments/sess_123":
			json.NewEncoder(w).Encode(map[string]any{
				"id":             "sess_123",
				"name":           "demo",
				"state":          "healthy",
				"package_ref":    "@telos/demo:1.2.3",
				"package_digest": pkg.Digest,
				"created_at":     "then",
				"updated_at":     "now",
			})
		case "/api/packages/telos/demo/versions/1.2.3/bundle":
			_, _ = w.Write(pkg.Bytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := packageForSession(cloud.NewClient(srv.URL, "token"), "sess_123")
	if err != nil {
		t.Fatal(err)
	}
	if got.reference.ref != "@telos/demo:1.2.3" || got.digest != pkg.Digest {
		t.Fatalf("package: got %+v", got)
	}
}

func TestPackageForReferenceUsesRegistryDigest(t *testing.T) {
	pkg := testApplyPackage(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/packages/telos/demo/versions/1.2.3":
			json.NewEncoder(w).Encode(map[string]any{
				"scope":      "telos",
				"name":       "demo",
				"version":    "1.2.3",
				"ref":        "@telos/demo:1.2.3",
				"digest":     pkg.Digest,
				"created_at": "now",
			})
		case "/api/packages/telos/demo/versions/1.2.3/bundle":
			_, _ = w.Write(pkg.Bytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	reference, err := parsePackageReference("@telos/demo:1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	got, err := packageForReference(cloud.NewClient(srv.URL, "token"), reference)
	if err != nil {
		t.Fatal(err)
	}
	if got.digest != pkg.Digest || string(got.data) != string(pkg.Bytes) {
		t.Fatalf("package: got %+v", got)
	}
}

func TestCmdApplyUsesExactRegistryPackageWithoutRepublishing(t *testing.T) {
	pkg := testApplyPackage(t)
	var published bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/packages/telos/demo/versions/1.2.3":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"scope":      "telos",
				"name":       "demo",
				"version":    "1.2.3",
				"ref":        "@telos/demo:1.2.3",
				"digest":     pkg.Digest,
				"created_at": "now",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/packages/telos/demo/versions/1.2.3/bundle":
			_, _ = w.Write(pkg.Bytes)
		case r.Method == http.MethodPost && r.URL.Path == "/api/packages":
			published = true
			http.Error(w, "unexpected publish", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/api/deployments":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["name"] != "demo" || request["package_ref"] != "@telos/demo:1.2.3" {
				t.Fatalf("deployment request = %#v", request)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             "sess_registry",
				"name":           "demo",
				"state":          "provisioning",
				"package_ref":    "@telos/demo:1.2.3",
				"package_digest": pkg.Digest,
				"created_at":     "then",
				"updated_at":     "now",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	configureCloudTest(t, srv.URL)
	t.Setenv("TELOS_CONTEXT", "")

	out := captureStdout(t, func() {
		cmdApply([]string{"@telos/demo:1.2.3", "--json"})
	})
	if published {
		t.Fatal("registry apply republished the package")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("apply JSON: %v\n%s", err, out)
	}
	if result["operation"] != "created" || result["context"] != "personal" {
		t.Fatalf("apply result = %#v", result)
	}
}

func TestRegistryPackageForApplyRejectsDigestMismatch(t *testing.T) {
	pkg := testApplyPackage(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/bundle"):
			_, _ = w.Write(pkg.Bytes)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"scope":   "telos",
				"name":    "demo",
				"version": "1.2.3",
				"ref":     "@telos/demo:1.2.3",
				"digest":  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			})
		}
	}))
	defer srv.Close()
	reference, err := parsePackageReference("@telos/demo:1.2.3")
	if err != nil {
		t.Fatal(err)
	}

	_, err = registryPackageForApply(cloud.NewClient(srv.URL, "token"), reference)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest mismatch error = %v", err)
	}
}

func TestMaterializePackageDirectoryAndMarkdown(t *testing.T) {
	pkg := testApplyPackage(t)
	pulled := &pulledPackage{
		reference: packageReference{
			scope:   "telos",
			name:    "demo",
			version: "1.2.3",
			ref:     "@telos/demo:1.2.3",
		},
		digest: pkg.Digest,
		data:   pkg.Bytes,
	}
	root := t.TempDir()
	dir := filepath.Join(root, "package")
	path, err := materializePackage(cloud.NewClient("", ""), pulled, dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != dir {
		t.Fatalf("path: got %q want %q", path, dir)
	}
	assertFileContains(t, filepath.Join(dir, "SPEC.md"), "name: demo")
	assertFileContains(t, filepath.Join(dir, "manifest.json"), `"schema_version": 1`)

	markdown := filepath.Join(root, "copy.md")
	path, err = materializePackage(cloud.NewClient("", ""), pulled, markdown)
	if err != nil {
		t.Fatal(err)
	}
	if path != markdown {
		t.Fatalf("path: got %q want %q", path, markdown)
	}
	assertFileContains(t, markdown, "name: demo")

	if _, err := materializePackage(cloud.NewClient("", ""), pulled, markdown); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error: %v", err)
	}
}

func TestMaterializePackageRejectsDigestMismatch(t *testing.T) {
	pkg := testApplyPackage(t)
	pulled := &pulledPackage{
		reference: packageReference{
			scope:   "telos",
			name:    "demo",
			version: "1.2.3",
			ref:     "@telos/demo:1.2.3",
		},
		digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		data:   pkg.Bytes,
	}
	_, err := materializePackage(
		cloud.NewClient("", ""),
		pulled,
		filepath.Join(t.TempDir(), "package"),
	)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest mismatch error: %v", err)
	}
}

func testApplyPackage(t *testing.T) *spec.ApplyPackage {
	t.Helper()
	root := t.TempDir()
	specPath := filepath.Join(root, "SPEC.md")
	if err := os.WriteFile(specPath, []byte(`---
name: demo
version: 1.2.3
platform: cloud
---

# Goal

Serve a demo.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func assertFileContains(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s missing %q:\n%s", path, want, data)
	}
}
