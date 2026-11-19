package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
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
	if !preview.DryRun || preview.Context == "" || !strings.Contains(strings.Join(preview.Notes, " "), "server-side checks") {
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
			if err := os.WriteFile(os.Getenv(config.ConfigPathEnv), []byte("default_model: ignored/legacy\n"), 0o600); err != nil {
				t.Fatal(err)
			}
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

func TestDryRunSpecUpdatePreservesSettingsAndDetectsUnchanged(t *testing.T) {
	pkg := testApplyPackage(t)
	server := inferenceTestServer(t, registryApplyHandlers(pkg))
	defer server.Close()
	configureDryRunTest(t, server.URL)
	t.Setenv("TELOS_MODEL", "invalid/model")
	t.Setenv("TELOS_THINKING", "invalid")
	preview := decodeApplyPreview(t, "@telos/demo:1.2.3", "--goal", "goal_test", "--force")
	if preview.Operation != "unchanged" || preview.Current == nil || !preview.Force || preview.Proposed.ModelSource != "goal" || preview.Proposed.ThinkingSource != "goal" || preview.SpecDiff != "" {
		t.Fatalf("preview = %+v", preview)
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

func TestDryRunRequiresLoginAndIsNotAvailableOnRun(t *testing.T) {
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_ID", "")
	stdout, stderr, err := inferenceCLIProcess(t, "apply", "@telos/demo:1.2.3", "--dry-run")
	if err == nil || stdout != "" || !strings.Contains(stderr, "run `telos login` first") {
		t.Fatalf("unauthenticated preview: error=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
	stdout, stderr, err = inferenceCLIProcess(t, "run", "SPEC.md", "--dry-run")
	if err == nil || stdout != "" || !strings.Contains(stderr, "flag provided but not defined: -dry-run") {
		t.Fatalf("local dry run: error=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
}

func TestDryRunSettingsUsesDesiredStateAndIgnoresCreationDefaults(t *testing.T) {
	server := inferenceTestServer(t, map[string]http.HandlerFunc{
		"GET /api/deployments/goal_test/inference": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"selection":{"source":"managed","tier":"default"},"inference":{"source":"managed","tier":"default"},"agent_model":"telos-bifrost/telos/default","agent_thinking":"high","generation":3,"applied":false}`))
		},
	})
	defer server.Close()
	configureInferenceApplyTest(t, server.URL)
	for _, tt := range []struct {
		args                       []string
		operation, model, thinking string
	}{
		{[]string{"--model", "telos/max"}, "update", "telos/max", "high"},
		{[]string{"--thinking", "off"}, "update", "telos/default", "off"},
		{[]string{"--model", "telos/default", "--thinking", "high"}, "unchanged", "telos/default", "high"},
	} {
		preview := decodeApplyPreview(t, append([]string{"--goal", "goal_test"}, tt.args...)...)
		if preview.Operation != tt.operation || preview.Proposed.Model != tt.model || preview.Proposed.Thinking != tt.thinking || preview.Current.Thinking != "high" {
			t.Fatalf("preview = %+v", preview)
		}
	}
}

func TestDryRunLocalSpecUsesPublishedPackageIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(path, []byte("---\nname: demo\nversion: 1.2.3\ninterval: 6h\n---\n# Goal\nServe a demo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := spec.BuildApplyPackageWithSkillRefs(compiled, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	handlers := registryApplyHandlers(pkg)
	handlers["GET /api/packages/telos/demo/versions/1.2.4"] = http.NotFound
	handlers["GET /api/account/bootstrap"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[{"id":"org_personal","handle":"telos","default_publish_scope":"telos"}]}`))
	}
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	preview := decodeApplyPreview(t, path, "--goal", "goal_test")
	if preview.Operation != "unchanged" || preview.Proposed.Digest != pkg.Digest || *preview.Proposed.IntervalSeconds != 21600 {
		t.Fatalf("preview = %+v", preview)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(mustReadFile(t, path)), "Serve a demo.", "Serve a changed demo.")), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := inferenceCLIProcess(t, "apply", path, "--goal", "goal_test", "--dry-run")
	if err == nil || stdout != "" || !strings.Contains(stderr, "bump version in SPEC.md") {
		t.Fatalf("unbumped spec preview: error=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(mustReadFile(t, path)), "1.2.3", "1.2.4")), 0o644); err != nil {
		t.Fatal(err)
	}
	preview = decodeApplyPreview(t, path, "--goal", "goal_test")
	if preview.Operation != "update" || !strings.Contains(preview.SpecDiff, "+Serve a changed demo.") {
		t.Fatalf("preview = %+v", preview)
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

func TestDryRunTextEndsWithNothingApplied(t *testing.T) {
	server := inferenceTestServer(t, registryApplyHandlers(testApplyPackage(t)))
	defer server.Close()
	configureDryRunTest(t, server.URL)
	output := captureStdout(t, func() { cmdApply([]string{"@telos/demo:1.2.3", "--dry-run"}) })
	for _, expected := range []string{"Operation create", "Model     Work Anthropic/claude-test (workspace default)", "Thinking  cloud default (resolved on apply)"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q:\n%s", expected, output)
		}
	}
	if !strings.HasSuffix(output, "Nothing was applied.\n") {
		t.Fatalf("preview ending:\n%s", output)
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

func TestDryRunUsesCanonicalContext(t *testing.T) {
	server := inferenceTestServer(t, registryApplyHandlers(testApplyPackage(t)))
	defer server.Close()
	configureDryRunTest(t, server.URL)
	for _, tt := range []struct{ stored, want string }{
		{"org_personal", "personal"}, {"org_telos", "@telos"},
	} {
		if err := config.SaveConfig(&config.Config{Context: tt.stored}); err != nil {
			t.Fatal(err)
		}
		preview := decodeApplyPreview(t, "@telos/demo:1.2.3", "--model", "telos/default", "--thinking", "high")
		if preview.Context != tt.want {
			t.Fatalf("context = %q, want %q", preview.Context, tt.want)
		}
	}
}

func TestPlanCommandIsRemoved(t *testing.T) {
	stdout, stderr, err := inferenceCLIProcess(t, "plan", "SPEC.md")
	if err == nil || stdout != "" || !strings.Contains(stderr, "unknown command: plan") || strings.Contains(stderr, "plan SPEC.md") {
		t.Fatalf("removed plan: error=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
}
