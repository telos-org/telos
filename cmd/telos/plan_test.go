package main

import (
	"bytes"
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

func TestCompareCloudSessionSpecShowsDeployedDiff(t *testing.T) {
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

	proposed := []byte(`---
name: demo
version: 1.2.4
platform: cloud
---

# Goal

Serve an updated demo.
`)
	comparison, err := compareCloudSessionSpec(
		cloud.NewClient(srv.URL, "token"),
		"sess_123",
		proposed,
	)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.currentRef != "@telos/demo:1.2.3" {
		t.Fatalf("current ref: got %q", comparison.currentRef)
	}
	for _, want := range []string{
		"--- deployed/SPEC.md",
		"+++ proposed/SPEC.md",
		"-version: 1.2.3",
		"+version: 1.2.4",
		"-Serve a demo.",
		"+Serve an updated demo.",
	} {
		if !strings.Contains(comparison.diff, want) {
			t.Fatalf("diff missing %q:\n%s", want, comparison.diff)
		}
	}
}

func TestPrintPlanPreviewShowsSessionDiff(t *testing.T) {
	compiled := &spec.CompiledEnvironment{
		Environment: &spec.EnvironmentSpec{Name: "demo"},
		ContentHash: "8a8f0c21",
	}
	comparison := newSpecComparison(
		"sess_123",
		"@telos/demo:1.2.3",
		[]byte("# Goal\n\nOld.\n"),
		[]byte("# Goal\n\nNew.\n"),
	)

	var out bytes.Buffer
	printPlanPreview(&out, compiled, "./SPEC.md", "cloud", "personal", "root", comparison)
	text := out.String()
	for _, want := range []string{
		"Session   sess_123",
		"Current   @telos/demo:1.2.3",
		"--- deployed/SPEC.md",
		"+++ proposed/SPEC.md",
		"-Old.",
		"+New.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan output missing %q:\n%s", want, text)
		}
	}
}

func TestPrintPlanPreviewShowsNoSpecChanges(t *testing.T) {
	compiled := &spec.CompiledEnvironment{
		Environment: &spec.EnvironmentSpec{Name: "demo"},
		ContentHash: "8a8f0c21",
	}
	markdown := []byte("# Goal\n\nUnchanged.\n")
	comparison := newSpecComparison(
		"sess_123",
		"@telos/demo:1.2.3",
		markdown,
		markdown,
	)

	var out bytes.Buffer
	printPlanPreview(&out, compiled, "./SPEC.md", "cloud", "personal", "root", comparison)
	if !strings.Contains(out.String(), "No spec changes.") {
		t.Fatalf("plan output:\n%s", out.String())
	}
}

func TestPlanSessionJSONReportsUpdateWithoutCreate(t *testing.T) {
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
	configureCloudTest(t, srv.URL)

	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	markdown, _, err := spec.ApplyPackageSpec(pkg.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, markdown, 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		cmdPlan([]string{specPath, "--session", "sess_123", "--json"})
	})
	var plan struct {
		Target struct {
			WillCreate bool `json:"will_create_session"`
			WillUpdate bool `json:"will_update_session"`
		} `json:"target"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Target.WillCreate || !plan.Target.WillUpdate {
		t.Fatalf("target: got %+v", plan.Target)
	}
}
