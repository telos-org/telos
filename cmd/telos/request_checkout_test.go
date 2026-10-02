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

func TestGetRequestChecksOutItsExactProposalAndTracksIt(t *testing.T) {
	pkg := testApplyPackage(t)
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID = 3, "cp_third"
	request.PackageRef, request.PackageDigest = "@telos/demo:1.2.3", pkg.Digest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("checkout mutated Cloud: %s %s", r.Method, r.URL.Path)
		}
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/change-requests/cr_saved":
			_ = json.NewEncoder(w).Encode(request)
		case "/api/packages/telos/demo/versions/1.2.3/bundle":
			_, _ = w.Write(pkg.Bytes)
		default:
			t.Errorf("checkout read a different package: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	destination := filepath.Join(t.TempDir(), "request")
	out := captureStdout(t, func() {
		cmdGet([]string{"cr_saved", "--output", destination})
	})
	if !strings.Contains(out, "cr_saved Update 3") {
		t.Fatalf("missing exact request in checkout receipt: %s", out)
	}
	assertFileContains(t, filepath.Join(destination, "SPEC.md"), "name: demo")
	var tracked bool
	err := filepath.WalkDir(filepath.Join(destination, ".telos"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tracked = tracked || (strings.Contains(string(data), "cr_saved") && strings.Contains(string(data), "cp_third"))
		}
		return nil
	})
	if err != nil || !tracked {
		t.Fatalf("checkout did not pin request ancestry: tracked=%v err=%v", tracked, err)
	}
}

func TestRequestCheckoutSkillRemovalIsAuthoritative(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "custom/check"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "custom/check/SKILL.md"), []byte("---\nname: check\n---\nCheck health.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	markdown := "---\nname: demo\nversion: 1.0.0\nplatform: cloud\nskills: [\"./custom/check*\"]\n---\n\nKeep the exact body.\n"
	if err := os.WriteFile(filepath.Join(source, "SPEC.md"), []byte(markdown), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(filepath.Join(source, "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		t.Fatal(err)
	}
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID = 1, "cp_first"
	request.PackageRef, request.PackageDigest = "@telos/demo:1.0.0", pkg.Digest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/change-requests/cr_saved":
			_ = json.NewEncoder(w).Encode(request)
		case "/api/packages/telos/demo/versions/1.0.0/bundle":
			_, _ = w.Write(pkg.Bytes)
		default:
			t.Errorf("unexpected checkout call: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	control, err := cloud.ControlClientForContext("")
	if err != nil {
		t.Fatal(err)
	}
	_, destination, err := checkoutRequest(control, request.ID, filepath.Join(t.TempDir(), "checkout"))
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(destination, "SPEC.md")
	assertFileContains(t, specPath, "./skills/check*")
	assertFileContains(t, specPath, "\n\nKeep the exact body.\n")
	assertFileContains(t, filepath.Join(destination, "skills/check/SKILL.md"), "Check health.")
	if _, err := os.Stat(filepath.Join(destination, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("archive manifest remains in editable checkout: %v", err)
	}
	data, _ := os.ReadFile(specPath)
	withoutSkill, err := rewriteMergeSpec(string(data), map[string]string{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, []byte(withoutSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, err := packageSpec(specPath, "")
	if err != nil || len(updated.compiled.Skills) != 0 {
		t.Fatalf("removed skill was reintroduced: package=%+v err=%v", updated, err)
	}
}

func TestRequestCheckoutRejectsMismatchedSourcesAndExistingFiles(t *testing.T) {
	for _, scenario := range []string{"different request", "different digest", "existing directory", "spec-only output"} {
		t.Run(scenario, func(t *testing.T) {
			pkg := testApplyPackage(t)
			request := testDeploymentPlan("saved", "awaiting_confirmation")
			request.UpdateNumber, request.PreparedPlanID = 1, "cp_first"
			request.PackageRef, request.PackageDigest = "@telos/demo:1.2.3", pkg.Digest
			if scenario == "different request" {
				request.ID = "cr_other"
			}
			if scenario == "different digest" {
				request.PackageDigest = "sha256:" + strings.Repeat("0", 64)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/change-requests/cr_saved":
					_ = json.NewEncoder(w).Encode(request)
				case "/api/packages/telos/demo/versions/1.2.3/bundle":
					_, _ = w.Write(pkg.Bytes)
				default:
					t.Errorf("unexpected checkout call: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			destination := filepath.Join(t.TempDir(), "request")
			if scenario == "spec-only output" {
				destination += ".md"
			}
			if scenario == "existing directory" {
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(destination, "SPEC.md"), []byte("unsubmitted local draft"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := checkoutRequest(cloud.NewClient(server.URL, "token"), "cr_saved", destination); err == nil {
				t.Fatal("unsafe checkout succeeded")
			}
			if scenario == "existing directory" {
				assertFileContains(t, filepath.Join(destination, "SPEC.md"), "unsubmitted local draft")
			} else if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatalf("failed checkout wrote files: %v", err)
			}
		})
	}
}
