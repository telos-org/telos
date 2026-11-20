package main

import (
	"bytes"
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

func configureDryRunTest(t *testing.T, endpoint string) {
	t.Helper()
	configureCloudTest(t, endpoint)
	for _, key := range []string{"TELOS_CONTEXT", "TELOS_MODEL", "TELOS_THINKING", "TELOS_SESSION_ID", "TELOS_RUNTIME", "TELOS_API_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("TELOS_SKILLS_DIR", t.TempDir())
	t.Setenv(spec.RegistrySkillsDirEnv, t.TempDir())
}

func decodeApplyPreview(t *testing.T, args ...string) applyPreview {
	t.Helper()
	output := captureStdout(t, func() { cmdApply(append(args, "--dry-run", "--json")) })
	var preview applyPreview
	if err := json.Unmarshal([]byte(output), &preview); err != nil {
		t.Fatal(err)
	}
	notes := strings.ToLower(strings.Join(preview.Notes, " "))
	if !preview.DryRun || preview.Context == "" || !strings.Contains(notes, "server-side checks") {
		t.Fatalf("incomplete preview: %s", output)
	}
	return preview
}

func registryApplyHandlers(pkg *spec.ApplyPackage) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /api/packages/telos/demo/versions/1.2.3": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(cloud.PackageVersionRecord{Scope: "telos", Name: "demo", Version: "1.2.3", Ref: "@telos/demo:1.2.3", Digest: pkg.Digest})
		},
		"GET /api/packages/telos/demo/versions/1.2.3/bundle": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(pkg.Bytes)
		},
		"GET /api/deployments/goal_test": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(cloud.SessionRecord{ID: "goal_test", Name: "demo", PackageRef: "@telos/demo:1.2.3", PackageDigest: pkg.Digest})
		},
	}
}

func TestDryRunCreationInferencePrecedence(t *testing.T) {
	pkg := testApplyPackage(t)
	for _, tt := range []struct {
		name, envModel, envThinking, preference, wantModel, wantThinking string
		modelSource, thinkingSource                                      previewSettingSource
		flags                                                            []string
	}{
		{"workspace defaults", "", "", `{"selection":{"source":"managed","tier":"max"},"agent_thinking":"high"}`, "telos/max", "high", "workspace", "workspace", nil},
		{"workspace subscription and native thinking", "", "", `{"selection":{"source":"subscription","connection_id":"sub_work","model":"gpt-test"},"agent_thinking":"off"}`, "My ChatGPT/gpt-test", "off", "workspace", "workspace", nil},
		{"environment overrides", "telos/default", "low", "", "telos/default", "low", "override", "override", nil},
		{"flags override environment", "telos/default", "low", "", "telos/max", "high", "override", "override", []string{"--model", "telos/max", "--thinking", "high"}},
		{"empty flag inherits workspace", "telos/default", "", `{"selection":{"source":"managed","tier":"max"},"agent_thinking":"high"}`, "telos/max", "high", "workspace", "workspace", []string{"--model", ""}},
		{"explicit model inherits workspace thinking", "", "", `{"selection":{"source":"managed","tier":"default"},"agent_thinking":"high"}`, "telos/max", "high", "override", "workspace", []string{"--model", "telos/max"}},
		{"explicit thinking inherits workspace model", "", "", `{"selection":{"source":"byok","connection_id":"key_work","model":"claude-test"},"agent_thinking":"high"}`, "Work Anthropic/claude-test", "low", "workspace", "override", []string{"--thinking", "low"}},
		{"cloud thinking remains unresolved", "", "", `{"selection":{"source":"managed","tier":"default"},"agent_thinking":null}`, "telos/default", "", "workspace", "cloud", nil},
		{"named subscription", "", "", "", "My ChatGPT/gpt-test", "high", "override", "override", []string{"--model", "My ChatGPT/gpt-test", "--thinking", "high"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handlers := registryApplyHandlers(pkg)
			handlers["GET /api/inference/preference"] = func(w http.ResponseWriter, r *http.Request) {
				if tt.preference == "" {
					t.Error("explicit settings should not fetch workspace defaults")
				}
				_, _ = w.Write([]byte(tt.preference))
			}
			// This server rejects every route not listed, including every mutating request.
			server := inferenceTestServer(t, handlers)
			defer server.Close()
			configureDryRunTest(t, server.URL)
			t.Setenv("TELOS_MODEL", tt.envModel)
			t.Setenv("TELOS_THINKING", tt.envThinking)
			preview := decodeApplyPreview(t, append([]string{"@telos/demo:1.2.3"}, tt.flags...)...)
			state := preview.Proposed
			if preview.Operation != "create" || state.Model != tt.wantModel || state.Thinking != tt.wantThinking || state.ModelSource != tt.modelSource || state.ThinkingSource != tt.thinkingSource {
				t.Fatalf("preview = %+v", preview)
			}
			if !strings.Contains(state.Spec, "Serve a demo") || state.Digest != pkg.Digest {
				t.Fatalf("missing resolved package: %+v", state)
			}
		})
	}
}

func TestApplyChecksResolvedPackageVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version, previewOperation, wantError string
		status                                     int
	}{
		{name: "missing version", version: "1.2.4", status: http.StatusNotFound, previewOperation: "update"},
		{name: "identical digest", version: "1.2.3", status: http.StatusOK, previewOperation: "unchanged"},
		{
			name: "different digest", version: "1.2.3", status: http.StatusOK,
			wantError: "package version @telos/demo:1.2.3 already exists; bump version in SPEC.md",
		},
		{
			name: "read denied", version: "1.2.3", status: http.StatusForbidden,
			wantError: "check package version @telos/demo:1.2.3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := makeDryRunSkillFixture(t)
			const currentVersion = "1.2.3"
			if tc.version != currentVersion {
				markdown := strings.Replace(string(mustReadFile(t, fixture.path)), currentVersion, tc.version, 1)
				if err := os.WriteFile(fixture.path, []byte(markdown), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			const skillRef = "@telos/verify-test:1.0.0"
			ref := "@telos/demo:" + tc.version
			var writes atomic.Int32
			handlers := registryApplyHandlers(fixture.pkg)
			handlers["GET /api/account/bootstrap"] = dryRunPublishScope
			handlers["GET /api/skills/telos/verify-test"] = jsonResponse(cloud.SkillRecord{
				Scope: "telos", Name: "verify-test", Version: "1.0.0", Ref: skillRef, Digest: fixture.digest,
			})
			handlers["GET /api/packages/telos/demo/versions/"+tc.version] = func(w http.ResponseWriter, r *http.Request) {
				if tc.status != http.StatusOK {
					http.Error(w, http.StatusText(tc.status), tc.status)
					return
				}
				digest := fixture.pkg.Digest
				if tc.wantError != "" {
					digest = "sha256:" + strings.Repeat("0", 64)
				}
				jsonResponse(cloud.PackageVersionRecord{
					Scope: "telos", Name: "demo", Version: tc.version, Ref: ref, Digest: digest,
				})(w, r)
			}
			for route, response := range map[string]any{
				"POST /api/skills":               cloud.SkillRecord{Ref: skillRef},
				"POST /api/packages":             cloud.PackageVersionRecord{Ref: ref},
				"PUT /api/deployments/goal_test": cloud.SessionRecord{ID: "goal_test", PackageRef: ref},
			} {
				handlers[route] = func(w http.ResponseWriter, r *http.Request) {
					writes.Add(1)
					jsonResponse(response)(w, r)
				}
			}
			server := inferenceTestServer(t, handlers)
			defer server.Close()
			configureDryRunTest(t, server.URL)
			for _, mode := range []string{"dry-run", "apply"} {
				t.Run(mode, func(t *testing.T) {
					writes.Store(0)
					args := []string{"apply", fixture.path, "--goal", "goal_test", "--json"}
					if mode == "dry-run" {
						args = append(args, "--dry-run")
					}
					out, stderr, err := inferenceCLIProcess(t, args...)
					if tc.wantError != "" {
						if err == nil || out != "" || !strings.Contains(stderr, tc.wantError) || writes.Load() != 0 {
							t.Fatalf("error=%v writes=%d stdout=%s stderr=%s", err, writes.Load(), out, stderr)
						}
						return
					}
					wantOperation, wantWrites := "updated", int32(3)
					if mode == "dry-run" {
						wantOperation, wantWrites = tc.previewOperation, 0
					}
					var result struct {
						Operation string `json:"operation"`
					}
					if err != nil || json.Unmarshal([]byte(out), &result) != nil ||
						result.Operation != wantOperation || writes.Load() != wantWrites {
						t.Fatalf("want %s and %d writes, got error=%v writes=%d stdout=%s stderr=%s",
							wantOperation, wantWrites, err, writes.Load(), out, stderr)
					}
				})
			}
		})
	}
}

func TestDryRunIdenticalContentsWithDifferentReferenceAreAnUpdate(t *testing.T) {
	pkg := testApplyPackage(t)
	handlers := registryApplyHandlers(pkg)
	handlers["GET /api/packages/other/demo/versions/1.2.3"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.PackageVersionRecord{Scope: "other", Name: "demo", Version: "1.2.3", Ref: "@other/demo:1.2.3", Digest: pkg.Digest})
	}
	handlers["GET /api/packages/other/demo/versions/1.2.3/bundle"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pkg.Bytes)
	}
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	preview := decodeApplyPreview(t, "@other/demo:1.2.3", "--goal", "goal_test")
	if preview.Operation != "update" || preview.Proposed.Digest != preview.Current.Digest || preview.Proposed.Ref == preview.Current.Ref || preview.SpecDiff != "" {
		t.Fatalf("reference change was hidden: %+v", preview)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDryRunSettingsTextOnlyShowsChangedValues(t *testing.T) {
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"GET /api/deployments/goal_test/inference": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"selection":{"source":"managed","tier":"default"},"inference":{"source":"managed","tier":"default"},"agent_model":"telos-bifrost/telos/default","agent_thinking":"high"}`))
		},
	})
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	for _, tt := range []struct {
		name, operation, model, thinking string
		args                             []string
		inherited                        bool
	}{
		{"thinking only", "update", "telos/default (Goal setting)", "high -> off", []string{"--thinking", "off"}, false},
		{"model only", "update", "telos/default -> telos/max", "high (Goal setting)", []string{"--model", "telos/max"}, true},
		{"unchanged", "unchanged", "telos/default", "high", []string{"--model", "telos/default", "--thinking", "high"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--goal", "goal_test", "--dry-run"}, tt.args...)
			output := captureStdout(t, func() { cmdApply(args) })
			lines := []string{
				"Operation " + tt.operation,
				"Context   personal",
				"Goal      goal_test",
				"Model     " + tt.model,
				"Thinking  " + tt.thinking,
				"",
				"Server-side checks run when applying.",
			}
			if tt.inherited {
				lines = append(lines, "The server resolves inherited thinking for the selected model when applying.")
			}
			lines = append(lines, "Nothing was applied.", "")
			if want := strings.Join(lines, "\n"); output != want {
				t.Fatalf("settings preview:\n%s\nwant:\n%s", output, want)
			}
		})
	}
}

func TestDryRunFileSummariesOnlyShowActualModeChanges(t *testing.T) {
	before := map[string]cloud.SkillFile{
		"removed": {Mode: "0644", Data: []byte("old")},
		"script":  {Mode: "0644", Data: []byte("run")},
	}
	after := map[string]cloud.SkillFile{
		"added":  {Mode: "0644", Data: []byte("new")},
		"script": {Mode: "0755", Data: []byte("run")},
	}
	var output bytes.Buffer
	printApplyPreview(&output, &applyPreview{SkillDiffs: diffSkillFiles("test", before, after)}, false)
	for _, expected := range []string{
		"added skills/test/added\n",
		"removed skills/test/removed\n",
		"modified skills/test/script (mode 0644 -> 0755)\n",
		"+++ /dev/null\n",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q:\n%s", expected, output.String())
		}
	}
}

type dryRunSkillFixture struct {
	path   string
	pkg    *spec.ApplyPackage
	digest string
	bundle []byte
}

func makeDryRunSkillFixture(t *testing.T) dryRunSkillFixture {
	t.Helper()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "verify-test")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: verify-test\n---\nVerify old behavior.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(path, []byte("---\nname: demo\nversion: 1.2.3\nskills: ['./verify-test*']\n---\n# Goal\nServe a demo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := spec.BuildApplyPackageWithSkillRefs(compiled, map[string]string{"verify-test": "@telos/verify-test:1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	digest, bundle, err := spec.BuildSkillBundle(compiled.Skills[0])
	if err != nil {
		t.Fatal(err)
	}
	return dryRunSkillFixture{path: path, pkg: pkg, digest: digest, bundle: bundle}
}

func TestDiffSkillFilesHandlesFileKindsAndModes(t *testing.T) {
	before := map[string]cloud.SkillFile{
		"removed.txt": {Mode: "0644", Data: []byte("old")},
		"script.sh":   {Mode: "0644", Data: []byte("run")},
		"unchanged":   {Mode: "0644", Data: []byte("same")},
	}
	after := map[string]cloud.SkillFile{
		"binary":    {Mode: "0644", Data: []byte{0, 1}},
		"empty":     {Mode: "0644"},
		"large":     {Mode: "0644", Data: []byte(strings.Repeat("x", 256*1024+1))},
		"script.sh": {Mode: "0755", Data: []byte("run")},
		"unchanged": {Mode: "0644", Data: []byte("same")},
	}
	diffs := diffSkillFiles("test", before, after)
	if len(diffs) != 5 || diffs[0].Path != "binary" || diffs[0].ContentOmitted != "binary content" || diffs[1].Path != "empty" || diffs[1].Change != "added" || diffs[2].ContentOmitted != "content exceeds 256 KiB" || diffs[3].Change != "removed" || diffs[4].OldMode != "0644" || diffs[4].NewMode != "0755" || diffs[4].Diff != "" {
		t.Fatalf("file changes = %+v", diffs)
	}
}

func dryRunPublishScope(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[{"id":"org_personal","default_publish_scope":"telos"}]}`))
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
		assertApplyRejected(t, input, `cannot change Goal goal_test spec name from "demo" to "other"`)
	}
}

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
	server.Close()

	// Once the version is taken by different content, both modes must stop before writing.
	handlers["GET /api/skills/telos/verify-test/versions/2.0.0"] = jsonResponse(
		cloud.SkillRecord{Digest: fixture.digest},
	)
	conflictServer := inferenceTestServer(t, handlers)
	defer conflictServer.Close()
	configureDryRunTest(t, conflictServer.URL)
	assertApplyRejected(t, fixture.path, "bump version in SKILL.md")
}

func TestApplyWithUnreadableDeployedBaseline(t *testing.T) {
	pkg := testApplyPackage(t)
	ref := "@telos/demo:1.2.4"
	record := cloud.PackageVersionRecord{
		Scope: "telos", Name: "demo", Version: "1.2.4", Ref: ref, Digest: pkg.Digest,
	}
	for _, tc := range []struct {
		name   string
		bundle http.HandlerFunc
	}{
		{"missing", http.NotFound},
		{"corrupt", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("invalid bundle")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handlers := registryApplyHandlers(pkg)
			handlers["GET /api/packages/telos/demo/versions/1.2.4"] = jsonResponse(record)
			handlers["GET /api/packages/telos/demo/versions/1.2.4/bundle"] =
				handlers["GET /api/packages/telos/demo/versions/1.2.3/bundle"]
			handlers["GET /api/packages/telos/demo/versions/1.2.3/bundle"] = tc.bundle
			var updates atomic.Int32
			handlers["PUT /api/deployments/goal_test"] = func(w http.ResponseWriter, r *http.Request) {
				updates.Add(1)
				_ = json.NewEncoder(w).Encode(cloud.SessionRecord{ID: "goal_test", PackageRef: ref})
			}
			server := inferenceTestServer(t, handlers)
			defer server.Close()
			configureDryRunTest(t, server.URL)
			out, stderr, err := inferenceCLIProcess(t, "apply", ref, "--goal", "goal_test", "--dry-run")
			if err == nil || out != "" || !strings.Contains(stderr, "read deployed baseline") || updates.Load() != 0 {
				t.Fatalf("dry-run error=%v writes=%d stdout=%s stderr=%s", err, updates.Load(), out, stderr)
			}
			out, stderr, err = inferenceCLIProcess(t, "apply", ref, "--goal", "goal_test", "--json")
			if err != nil || updates.Load() != 1 || !strings.Contains(out, `"operation": "updated"`) ||
				!strings.Contains(stderr, "warning: could not read deployed baseline") {
				t.Fatalf("apply error=%v writes=%d stdout=%s stderr=%s", err, updates.Load(), out, stderr)
			}
		})
	}
}

func jsonResponse(value any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func assertApplyRejected(t *testing.T, input, message string) {
	t.Helper()
	args := []string{"apply", input, "--goal", "goal_test"}
	for _, flags := range [][]string{args, append(args, "--dry-run")} {
		out, stderr, err := inferenceCLIProcess(t, flags...)
		if err == nil || out != "" || !strings.Contains(stderr, message) {
			t.Fatalf("%v: error=%v stdout=%s stderr=%s", flags, err, out, stderr)
		}
	}
}

func TestDryRunAndApplyRejectInvalidLaterSkillBeforePublishing(t *testing.T) {
	server := inferenceTestServer(t, nil)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	dir := t.TempDir()
	for _, name := range []string{"a-good", "z-invalid"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		version := "1.0.0"
		if name == "z-invalid" {
			version = "v1"
		}
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte("---\nname: "+name+"\nversion: "+version+"\n---\nInstructions.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(path, []byte("---\nname: demo\nversion: 1.0.0\nskills: [./a-good, ./z-invalid]\n---\n# Goal\nTest.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{false, true} {
		args := []string{"apply", path}
		if dryRun {
			args = append(args, "--dry-run")
		}
		stdout, stderr, err := inferenceCLIProcess(t, args...)
		if err == nil || stdout != "" || !strings.Contains(stderr, "must not start with v") {
			t.Fatalf("dry run %v: error=%v, stdout=%s, stderr=%s", dryRun, err, stdout, stderr)
		}
	}
}
