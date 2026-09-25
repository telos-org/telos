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
	var creates, updates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/packages/telos/demo/versions/1.2.3":
			_ = json.NewEncoder(w).Encode(map[string]string{"scope": "telos", "name": "demo", "version": "1.2.3", "ref": "@telos/demo:1.2.3", "digest": pkg.Digest})
		case "/api/packages/telos/demo/versions/1.2.3/bundle":
			_, _ = w.Write(pkg.Bytes)
		case "/api/deployments/sess_registry":
			_, _ = w.Write([]byte(`{"id":"sess_registry","package_ref":"@telos/demo:1.2.2","current_revision_id":"rev_7"}`))
		case "/api/deployment-plans":
			var options cloud.DeploymentPlanOptions
			_ = json.NewDecoder(r.Body).Decode(&options)
			if options.Mode != "apply" || !options.AutoConfirm {
				t.Errorf("unexpected mode: %+v", options)
			}
			if options.Create != nil {
				creates++
				if options.Create.Name != "demo" || options.Create.PackageRef != "@telos/demo:1.2.3" {
					t.Errorf("create=%+v", options.Create)
				}
			} else {
				updates++
				if options.DeploymentID != "sess_registry" || options.Update.PackageRef != "@telos/demo:1.2.3" || options.Update.ExpectedCurrentRevisionID != "rev_7" || !options.Update.Force {
					t.Errorf("update=%+v", options)
				}
			}
			_ = json.NewEncoder(w).Encode(testDeploymentPlan("apply", "applied"))
		default:
			t.Errorf("registry apply tried to republish or bypass the plan API: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv("TELOS_CONTEXT", "")
	for _, args := range [][]string{
		{"@telos/demo:1.2.3", "--json", "--yes", "--message", "Deploy the reading list"},
		{"@telos/demo:1.2.3", "--session", "sess_registry", "--force", "--json", "-y", "-m", "Deploy the reading list"},
	} {
		out := captureStdout(t, func() { cmdApply(args) })
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil || result["operation"] != "applied" || result["context"] != "personal" {
			t.Fatalf("receipt=%s err=%v", out, err)
		}
	}
	if creates != 1 || updates != 1 {
		t.Fatalf("creates=%d updates=%d", creates, updates)
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
