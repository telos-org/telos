package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func TestApplyAllowsUnversionedSkillMatchingOlderRegistryVersion(t *testing.T) {
	for _, change := range []string{"unchanged", "spec", "skill", "rubric"} {
		for _, dryRun := range []bool{false, true} {
			mode := "apply"
			if dryRun {
				mode = "dry-run"
			}
			t.Run(change+"/"+mode, func(t *testing.T) {
				fixture := makeDryRunSkillFixture(t)
				switch change {
				case "spec":
					data := strings.ReplaceAll(string(mustReadFile(t, fixture.path)), "Serve a demo.", "Serve a changed demo.")
					if err := os.WriteFile(fixture.path, []byte(data), 0o644); err != nil {
						t.Fatal(err)
					}
				case "skill":
					path := filepath.Join(filepath.Dir(fixture.path), "verify-test", "SKILL.md")
					if err := os.WriteFile(path, []byte("---\nname: verify-test\n---\nChanged instructions.\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				case "rubric":
					data := strings.ReplaceAll(string(mustReadFile(t, fixture.path)), "verify-test*", "verify-test")
					if err := os.WriteFile(fixture.path, []byte(data), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				var writes atomic.Int32
				handlers := registryApplyHandlers(fixture.pkg)
				handlers["GET /api/account/bootstrap"] = dryRunPublishScope
				handlers["GET /api/skills/telos/verify-test"] = func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(cloud.SkillRecord{
						Scope: "telos", Name: "verify-test", Version: "2.0.0",
						Ref: "@telos/verify-test:2.0.0", Digest: "sha256:newer-different-content",
					})
				}
				handlers["POST /api/skills"] = func(w http.ResponseWriter, r *http.Request) {
					writes.Add(1)
					if change != "unchanged" {
						t.Error("published a changed skill before rejecting the package")
					}
					_ = json.NewEncoder(w).Encode(cloud.SkillRecord{Ref: "@telos/verify-test:1.0.0"})
				}
				handlers["POST /api/packages"] = func(w http.ResponseWriter, r *http.Request) {
					writes.Add(1)
					var body struct {
						Data string `json:"data_base64"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					data, err := base64.StdEncoding.DecodeString(body.Data)
					if err != nil {
						t.Error(err)
					}
					_, manifest, err := spec.ApplyPackageSpec(data)
					if err != nil {
						t.Error(err)
						http.Error(w, "invalid package", 400)
						return
					}
					if spec.ApplyPackageDigest(manifest) != fixture.pkg.Digest {
						t.Error("an unchanged apply changed the package digest")
					}
					_ = json.NewEncoder(w).Encode(cloud.PackageVersionRecord{Ref: "@telos/demo:1.2.3", Digest: fixture.pkg.Digest})
				}
				handlers["PUT /api/deployments/goal_test"] = func(w http.ResponseWriter, r *http.Request) {
					writes.Add(1)
					http.Error(w, "already current", http.StatusConflict)
				}
				server := inferenceTestServer(t, handlers)
				defer server.Close()
				configureDryRunTest(t, server.URL)
				args := []string{"apply", fixture.path, "--goal", "goal_test", "--json"}
				if dryRun {
					args = append(args, "--dry-run")
				}
				stdout, stderr, err := inferenceCLIProcess(t, args...)
				if change != "unchanged" {
					if err == nil || stdout != "" || !strings.Contains(stderr, "bump version in SPEC.md") || writes.Load() != 0 {
						t.Fatalf("changed inputs: error=%v writes=%d stdout=%s stderr=%s", err, writes.Load(), stdout, stderr)
					}
					return
				}
				if err != nil {
					t.Fatalf("unchanged apply failed: %v %s", err, stderr)
				}
				if dryRun {
					if writes.Load() != 0 {
						t.Fatal("dry run sent a write")
					}
				} else if writes.Load() != 3 || !strings.Contains(stdout, `"operation": "unchanged"`) {
					t.Fatalf("idempotent apply: writes=%d output=%s", writes.Load(), stdout)
				}
			})
		}
	}
}

func TestDryRunDiffUsesLocalFilesForUnpublishedSkillVersion(t *testing.T) {
	fixture := makeDryRunSkillFixture(t)
	data := strings.ReplaceAll(string(mustReadFile(t, fixture.path)), "1.2.3", "1.2.4")
	if err := os.WriteFile(fixture.path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(fixture.path), "verify-test", "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: verify-test\nversion: 2.0.0\n---\nVerify new behavior.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handlers := registryApplyHandlers(fixture.pkg)
	handlers["GET /api/account/bootstrap"] = dryRunPublishScope
	handlers["GET /api/packages/telos/demo/versions/1.2.4"] = http.NotFound
	handlers["GET /api/skills/telos/verify-test/versions/2.0.0"] = http.NotFound
	handlers["GET /api/skills/telos/verify-test/versions/1.0.0"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.SkillRecord{Digest: fixture.digest})
	}
	handlers["GET /api/skills/telos/verify-test/versions/1.0.0/bundle"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture.bundle)
	}
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	preview := decodeApplyPreview(t, fixture.path, "--goal", "goal_test", "--diff")
	if preview.Proposed.Digest == "" || preview.Proposed.Skills[0].Ref != "@telos/verify-test:2.0.0" {
		t.Fatalf("missing resolved identity: %+v", preview.Proposed)
	}
	if len(preview.SkillDiffs) != 1 || !strings.Contains(preview.SkillDiffs[0].Diff, "+Verify new behavior.") {
		t.Fatalf("unpublished skill diff is missing: %+v", preview)
	}
	if strings.Contains(strings.Join(preview.Notes, " "), "unavailable") {
		t.Fatalf("local skill diff attempted a Registry fetch: %v", preview.Notes)
	}
}
