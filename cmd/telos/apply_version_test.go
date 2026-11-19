package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func TestApplyPackageVersionChecksCreationAndUpdates(t *testing.T) {
	for _, goalID := range []string{"", "goal_test"} {
		for _, tt := range []struct {
			name, digest, wantError string
			status                  int
			pending                 bool
		}{
			{name: "new version", status: http.StatusNotFound},
			{name: "identical content"},
			{name: "conflict", digest: "sha256:different", wantError: "bump version in SPEC.md"},
			{name: "pending skills with changed spec", pending: true, wantError: "bump version in SPEC.md"},
			{name: "read denied", status: http.StatusForbidden, wantError: "check package version"},
		} {
			t.Run(goalID+"/"+tt.name, func(t *testing.T) {
				fixture := makeDryRunSkillFixture(t)
				specData := strings.ReplaceAll(string(mustReadFile(t, fixture.path)), "1.2.3", "1.2.4")
				if err := os.WriteFile(fixture.path, []byte(specData), 0o644); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(filepath.Dir(fixture.path), "verify-test", "SKILL.md")
				// Pin this local skill so the proposed package digest is known.
				if !tt.pending {
					data := strings.Replace(string(mustReadFile(t, path)), "name: verify-test", "name: verify-test\nversion: 1.0.0", 1)
					if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				compiled, err := spec.CompileEnvironment(fixture.path)
				if err != nil {
					t.Fatal(err)
				}
				proposed, err := spec.BuildApplyPackageWithSkillRefs(compiled, map[string]string{"verify-test": "@telos/verify-test:1.0.0"})
				if err != nil {
					t.Fatal(err)
				}
				handlers := registryApplyHandlers(fixture.pkg)
				handlers["GET /api/account/bootstrap"] = dryRunPublishScope
				handlers["GET /api/skills/telos/verify-test/versions/1.0.0"] = http.NotFound
				handlers["GET /api/skills/telos/verify-test"] = http.NotFound
				handlers["GET /api/packages/telos/demo/versions/1.2.4/bundle"] = func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write(fixture.pkg.Bytes)
				}
				handlers["GET /api/packages/telos/demo/versions/1.2.4"] = func(w http.ResponseWriter, r *http.Request) {
					if tt.status != 0 {
						http.Error(w, "registry read failed", tt.status)
						return
					}
					digest := proposed.Digest
					if tt.pending {
						digest = fixture.pkg.Digest
					}
					if tt.digest != "" {
						digest = tt.digest
					}
					_ = json.NewEncoder(w).Encode(cloud.PackageVersionRecord{Digest: digest})
				}
				server := inferenceTestServer(t, handlers)
				defer server.Close()
				configureDryRunTest(t, server.URL)
				modes := []bool{true}
				if tt.wantError != "" {
					modes = append(modes, false)
				}
				for _, dryRun := range modes {
					args := []string{"apply", fixture.path, "--json"}
					if dryRun {
						args = append(args, "--dry-run")
					}
					if goalID != "" {
						args = append(args, "--goal", goalID)
					}
					stdout, stderr, err := inferenceCLIProcess(t, args...)
					if tt.wantError != "" {
						if err == nil || stdout != "" || !strings.Contains(stderr, tt.wantError) {
							t.Fatalf("dry-run=%v error=%v stdout=%s stderr=%s", dryRun, err, stdout, stderr)
						}
					} else if err != nil {
						t.Fatalf("error=%v stderr=%s", err, stderr)
					}
				}
			})
		}
	}
}

func dryRunPublishScope(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[{"id":"org_personal","default_publish_scope":"telos"}]}`))
}

func TestApplyExplicitSkillVersionChecks(t *testing.T) {
	for _, tt := range []struct {
		name, digest, wantError string
		status                  int
	}{
		{name: "new version", status: http.StatusNotFound},
		{name: "identical content"},
		{name: "conflict", digest: "sha256:different", wantError: "bump version in SKILL.md"},
		{name: "read denied", status: http.StatusForbidden, wantError: "check skill version"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := makeDryRunSkillFixture(t)
			path := filepath.Join(filepath.Dir(fixture.path), "verify-test", "SKILL.md")
			data := strings.Replace(string(mustReadFile(t, path)), "name: verify-test", "name: verify-test\nversion: 1.0.0", 1)
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			skill, err := spec.LoadSkill(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			digest, _, err := spec.BuildSkillBundle(skill)
			if err != nil {
				t.Fatal(err)
			}
			handlers := registryApplyHandlers(fixture.pkg)
			handlers["GET /api/account/bootstrap"] = dryRunPublishScope
			handlers["GET /api/packages/telos/demo/versions/1.2.3"] = http.NotFound
			handlers["GET /api/skills/telos/verify-test/versions/1.0.0"] = func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 0 {
					http.Error(w, "registry read failed", tt.status)
					return
				}
				if tt.digest != "" {
					digest = tt.digest
				}
				_ = json.NewEncoder(w).Encode(cloud.SkillRecord{Digest: digest})
			}
			server := inferenceTestServer(t, handlers)
			defer server.Close()
			configureDryRunTest(t, server.URL)
			modes := []bool{true}
			if tt.wantError != "" {
				modes = append(modes, false)
			}
			for _, dryRun := range modes {
				args := []string{"apply", fixture.path, "--goal", "goal_test", "--json"}
				if dryRun {
					args = append(args, "--dry-run")
				}
				stdout, stderr, err := inferenceCLIProcess(t, args...)
				if tt.wantError != "" {
					if err == nil || stdout != "" || !strings.Contains(stderr, tt.wantError) {
						t.Fatalf("dry-run=%v error=%v stdout=%s stderr=%s", dryRun, err, stdout, stderr)
					}
				} else if err != nil {
					t.Fatalf("error=%v stderr=%s", err, stderr)
				}
			}
		})
	}
}

func TestApplyRejectsSpecNameChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(path, []byte("---\nname: other\nversion: 1.0.0\n---\n# Goal\nServe another service.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := spec.BuildApplyPackageWithSkillRefs(compiled, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	handlers := registryApplyHandlers(testApplyPackage(t))
	handlers["GET /api/account/bootstrap"] = dryRunPublishScope
	handlers["GET /api/packages/telos/other/versions/1.0.0"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.PackageVersionRecord{
			Scope: "telos", Name: "other", Version: "1.0.0",
			Ref: "@telos/other:1.0.0", Digest: proposed.Digest,
		})
	}
	handlers["GET /api/packages/telos/other/versions/1.0.0/bundle"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(proposed.Bytes)
	}
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	for _, input := range []string{path, "@telos/other:1.0.0"} {
		for _, dryRun := range []bool{false, true} {
			args := []string{"apply", input, "--goal", "goal_test"}
			if dryRun {
				args = append(args, "--dry-run")
			}
			stdout, stderr, err := inferenceCLIProcess(t, args...)
			if err == nil || stdout != "" || !strings.Contains(stderr, `cannot change Goal goal_test spec name from "demo" to "other"`) {
				t.Fatalf("dry-run=%v name change: error=%v stdout=%s stderr=%s", dryRun, err, stdout, stderr)
			}
		}
	}
}
