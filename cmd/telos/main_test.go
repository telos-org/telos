package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

func TestReorderInterspersedFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")
	fs.String("workspace", "", "")
	fs.String("until", "", "")

	got := reorderInterspersedFlags(fs, []string{
		"SPEC.md",
		"--json",
		"--workspace",
		"/tmp/ws",
		"--until=3",
	})
	want := []string{
		"--json",
		"--workspace",
		"/tmp/ws",
		"--until=3",
		"SPEC.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTopLevelUsageMentionsHelpAndVersion(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	text := out.String()
	for _, want := range []string{
		"usage: telos <command> [args]",
		"--help",
		"apply SPEC.md      Create or update a durable session from a spec",
		"delete SESSION     Delete a session (local history is preserved)",
		"version            Show version",
		"--version",
		"telos <command> --help",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing %q:\n%s", want, text)
		}
	}
}

func TestPrintPlanPreviewLocal(t *testing.T) {
	compiled := &spec.CompiledEnvironment{
		Environment: &spec.EnvironmentSpec{Name: "hello-service"},
		ContentHash: "8a8f0c21",
		Skills: []*spec.Skill{
			{Name: "verify-engineering"},
		},
	}

	var out bytes.Buffer
	printPlanPreview(&out, compiled, "./SPEC.md", "local", "root")
	text := out.String()
	for _, want := range []string{
		"Spec      hello-service",
		"Target    local",
		"Lineage   root",
		"Mutates   no",
		"Path      ./SPEC.md",
		"Hash      8a8f0c21",
		"Skills    verify-engineering",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan output missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"Namespace", "Plan for", "No sessions"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("plan output should not contain %q:\n%s", notWant, text)
		}
	}
}

func TestPrintPlanPreviewCloud(t *testing.T) {
	compiled := &spec.CompiledEnvironment{
		Environment: &spec.EnvironmentSpec{Name: "gitea"},
		Namespace:   "ns-gitea",
		ContentHash: "8a8f0c21",
		Skills: []*spec.Skill{
			{Name: "verify-engineering"},
			{Name: "verify-quality"},
		},
	}

	var out bytes.Buffer
	printPlanPreview(&out, compiled, "./SPEC.md", "cloud", "root")
	text := out.String()
	for _, want := range []string{
		"Spec      gitea",
		"Target    cloud",
		"Lineage   root",
		"Mutates   no",
		"Path      ./SPEC.md",
		"Namespace ns-gitea",
		"Hash      8a8f0c21",
		"Skills    verify-engineering, verify-quality",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan output missing %q:\n%s", want, text)
		}
	}
}

func TestReorderInterspersedFlagsDashDash(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")

	got := reorderInterspersedFlags(fs, []string{"--json", "--", "-literal"})
	want := []string{"--json", "-literal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestFlagNamesSetUsesExplicitFlagsOnly(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("thinking", "medium", "")
	fs.String("until", "", "")
	fs.String("workspace", "", "")
	parseFlags(fs, []string{"--thinking", "medium", "SPEC.md"})

	if !flagNamesSet(fs, "thinking") {
		t.Fatal("expected explicitly passed --thinking to be detected")
	}
	if flagNamesSet(fs, "until", "workspace") {
		t.Fatal("defaulted flags should not count as explicitly set")
	}
}

func TestResolveLocalRunConfigUsesEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("workspace", "", "")
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	parseFlags(fs, []string{"SPEC.md"})

	t.Setenv("TELOS_WORKSPACE", "/tmp/telos-workspace")
	t.Setenv("TELOS_MODEL", "claude-test")
	t.Setenv("TELOS_THINKING", "high")
	t.Setenv("TELOS_MAX_COST_USD", "12.5")

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "medium", 20.0)
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.Workspace != "/tmp/telos-workspace" {
		t.Fatalf("workspace: got %q", cfg.Workspace)
	}
	if cfg.Model != "claude-test" || cfg.Thinking != "high" {
		t.Fatalf("model/thinking: got %q/%q", cfg.Model, cfg.Thinking)
	}
	if cfg.MaxCostUSD == nil || *cfg.MaxCostUSD != 12.5 {
		t.Fatalf("cost: got %v", cfg.MaxCostUSD)
	}
}

func TestResolveLocalRunConfigUsesDefaultThinking(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("thinking", "", "")
	fs.Float64("max-cost-usd", 20.0, "")
	parseFlags(fs, []string{"SPEC.md"})

	cfg, err := resolveLocalRunConfigFromFlags(fs, "", "", "", 20.0)
	if err != nil {
		t.Fatalf("resolveLocalRunConfigFromFlags: %v", err)
	}
	if cfg.Thinking != cli.DefaultLocalThinking {
		t.Fatalf("thinking: got %q, want %q", cfg.Thinking, cli.DefaultLocalThinking)
	}
}

func TestResolveSessionRuntimeConfigUsesExplicitFlags(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	parseFlags(fs, []string{
		"--model", "openai-codex/gpt-5.5",
		"--thinking", "high",
		"--max-cost-usd", "100",
		"SPEC.md",
	})

	cfg, err := resolveSessionRuntimeConfigFromFlags(fs, "openai-codex/gpt-5.5", "high", 100)
	if err != nil {
		t.Fatalf("resolveSessionRuntimeConfigFromFlags: %v", err)
	}
	req := sessionapi.SessionCreateRequest{}
	applySessionRuntimeConfig(&req, cfg)
	if req.Model != "openai-codex/gpt-5.5" || req.Thinking != "high" {
		t.Fatalf("model/thinking: got %q/%q", req.Model, req.Thinking)
	}
	if req.MaxCostUSD == nil || *req.MaxCostUSD != 100 {
		t.Fatalf("max cost: got %v", req.MaxCostUSD)
	}
}

func TestResolveSessionRuntimeConfigOmitsDefaults(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("model", "", "")
	fs.String("thinking", "medium", "")
	fs.Float64("max-cost-usd", 20.0, "")
	parseFlags(fs, []string{"SPEC.md"})

	cfg, err := resolveSessionRuntimeConfigFromFlags(fs, "", "medium", 20.0)
	if err != nil {
		t.Fatalf("resolveSessionRuntimeConfigFromFlags: %v", err)
	}
	req := sessionapi.SessionCreateRequest{}
	applySessionRuntimeConfig(&req, cfg)
	if req.Model != "" || req.Thinking != "" || req.MaxCostUSD != nil {
		t.Fatalf("expected empty runtime request config, got %#v", req)
	}
}

func TestUntilFlagValue(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("until", "", "")
	parseFlags(fs, []string{"--until", "5", "SPEC.md"})

	got, err := untilFlagValue(fs, "5")
	if err != nil {
		t.Fatalf("untilFlagValue: %v", err)
	}
	if got.ReviewCycles != 5 || got.Seconds != 0 {
		t.Fatalf("until: got %#v", got)
	}
}

func TestUntilFlagValueDuration(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("until", "", "")
	parseFlags(fs, []string{"--until", "30m", "SPEC.md"})

	got, err := untilFlagValue(fs, "30m")
	if err != nil {
		t.Fatalf("untilFlagValue: %v", err)
	}
	if got.ReviewCycles != 0 || got.Seconds != 1800 {
		t.Fatalf("until: got %#v", got)
	}
}

func TestUntilFlagValueRejectsNonPositive(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("until", "", "")
	parseFlags(fs, []string{"--until", "0", "SPEC.md"})

	_, err := untilFlagValue(fs, "0")
	if err == nil {
		t.Fatal("expected --until 0 to fail")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUntilFlagValueRejectsSubsecondDuration(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.String("until", "", "")
	parseFlags(fs, []string{"--until", "500ms", "SPEC.md"})

	_, err := untilFlagValue(fs, "500ms")
	if err == nil {
		t.Fatal("expected --until 500ms to fail")
	}
	if !strings.Contains(err.Error(), "at least 1s") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecideLaunchModeMatchesPythonParity(t *testing.T) {
	tests := []struct {
		name            string
		platform        string
		cloudConfigured bool
		localConfigSet  bool
		want            launchMode
		wantErr         string
	}{
		{
			name:     "local spec runs locally",
			platform: "local",
			want:     launchLocal,
		},
		{
			name:           "local spec accepts local flags",
			platform:       "local",
			localConfigSet: true,
			want:           launchLocal,
		},
		{
			name:            "unspecified platform is cloud",
			cloudConfigured: true,
			want:            launchCloudApply,
		},
		{
			name:    "unspecified platform requires cloud login",
			wantErr: "runs in Telos Cloud",
		},
		{
			name:           "cloud rejects local flags",
			platform:       "cloud",
			localConfigSet: true,
			wantErr:        "local run config flags require a platform: local spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decideLaunchMode(
				tt.platform,
				tt.cloudConfigured,
				tt.localConfigSet,
			)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error: got %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("decideLaunchMode: %v", err)
			}
			if got != tt.want {
				t.Fatalf("mode: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionCreateRequestForLocalSpec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SPEC.md"), []byte("---\nname: demo\n---\n# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	req, err := sessionCreateRequestForSpec(dir)
	if err != nil {
		t.Fatalf("sessionCreateRequestForSpec: %v", err)
	}
	if req.SpecMarkdown == nil || !strings.Contains(*req.SpecMarkdown, "name: demo") {
		t.Fatalf("expected spec markdown, got %#v", req)
	}
}

func TestSessionKindForCommand(t *testing.T) {
	if got := sessionKindForCommand("apply"); got != sessionapi.KindController {
		t.Fatalf("apply kind: got %q", got)
	}
	if got := sessionKindForCommand("run"); got != sessionapi.KindTask {
		t.Fatalf("run kind: got %q", got)
	}
}

func TestValidateLaunchCommandRejectsCloudRunOutsideRoot(t *testing.T) {
	err := validateLaunchCommand("run", launchCloudApply)
	if err == nil {
		t.Fatal("expected cloud run rejection")
	}
	if !strings.Contains(err.Error(), "use telos apply") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateLaunchCommand("apply", launchCloudApply); err != nil {
		t.Fatalf("apply should be allowed: %v", err)
	}
	if err := validateLaunchCommand("run", launchLocal); err != nil {
		t.Fatalf("local run should be allowed: %v", err)
	}
}

func TestSessionCreateRequestRejectsCatalogueSpecID(t *testing.T) {
	_, err := sessionCreateRequestForSpec("cal-diy")
	if err == nil {
		t.Fatal("expected catalogue spec id to fail")
	}
	if !strings.Contains(err.Error(), "spec file not found: cal-diy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionCreateRequestRejectsMissingSpecPath(t *testing.T) {
	for _, input := range []string{"missing/SPEC.md", "../SPEC.md", "SPEC.md"} {
		t.Run(input, func(t *testing.T) {
			_, err := sessionCreateRequestForSpec(input)
			if err == nil {
				t.Fatal("expected missing local spec path to fail")
			}
			if !strings.Contains(err.Error(), "spec file not found") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPackageSpecBuildsApplyPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SPEC.md"), []byte("---\nversion: 1.2.0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, err := packageSpec(dir)
	if err != nil {
		t.Fatalf("packageSpec: %v", err)
	}
	if pkg.name != "postgres" {
		t.Fatalf("name: got %q", pkg.name)
	}
	if pkg.version != "1.2.0" {
		t.Fatalf("version: got %q", pkg.version)
	}
	if !strings.HasPrefix(pkg.digest, "sha256:") {
		t.Fatalf("digest: got %q", pkg.digest)
	}
	if len(pkg.bytes) == 0 {
		t.Fatal("missing package bytes")
	}
}

func TestPackageSkillDirBuildsSkillPublishPayload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: k8s-deploy\ndescription: Deploy\n---\nUse kubectl.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	scripts := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "deploy.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	skill, ok, err := packageSkillDir(dir, "1.0")
	if err != nil {
		t.Fatalf("packageSkillDir: %v", err)
	}
	if !ok {
		t.Fatal("expected skill directory")
	}
	if skill.name != "k8s-deploy" || skill.version != "1.0.0" {
		t.Fatalf("skill: got %+v", skill)
	}
	if string(skill.files["SKILL.md"].Data) != "---\nname: k8s-deploy\ndescription: Deploy\n---\nUse kubectl.\n" {
		t.Fatalf("skill file: got %#v", skill.files["SKILL.md"])
	}
	if skill.files["scripts/deploy.sh"].Mode != "0755" {
		t.Fatalf("script file: got %#v", skill.files["scripts/deploy.sh"])
	}
}

func TestPackageSkillDirAllowsRegistryAssignedVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: k8s-deploy\n---\nUse kubectl.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	skill, ok, err := packageSkillDir(dir, "")

	if !ok {
		t.Fatal("expected skill directory")
	}
	if err != nil {
		t.Fatalf("packageSkillDir: %v", err)
	}
	if skill.version != "" {
		t.Fatalf("version: got %q", skill.version)
	}
}

func TestPushSkillPackagePublishesSkill(t *testing.T) {
	var gotBody struct {
		Scope   string `json:"scope"`
		Name    string `json:"name"`
		Version string `json:"version"`
		Files   map[string]struct {
			DataBase64 string `json:"data_base64"`
			Mode       string `json:"mode"`
		} `json:"files"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/skills":
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"scope":      "telos",
				"name":       "k8s-deploy",
				"version":    "1.0.0",
				"ref":        "@telos/k8s-deploy:1.0.0",
				"digest":     "sha256:abc",
				"file_count": 1,
				"source_ref": "@telos/k8s-deploy:1.0.0",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	record, err := pushSkillPackage(
		cloud.NewClient(srv.URL, "test-token"),
		&skillPackage{
			name:    "k8s-deploy",
			version: "1.0.0",
			files: map[string]cloud.SkillFile{
				"SKILL.md": {Mode: "0644", Data: []byte("skill")},
			},
		},
		"telos",
	)
	if err != nil {
		t.Fatalf("pushSkillPackage: %v", err)
	}
	if record.Ref != "@telos/k8s-deploy:1.0.0" {
		t.Fatalf("record: got %+v", record)
	}
	if gotBody.Scope != "telos" || gotBody.Name != "k8s-deploy" || gotBody.Version != "1.0.0" {
		t.Fatalf("body: got %#v", gotBody)
	}
}

func TestPushPackageSkillsUsesPublishedPlatformCatalogueSkill(t *testing.T) {
	catalogue := t.TempDir()
	skillDir := filepath.Join(catalogue, "k8s-deploy")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: k8s-deploy\n---\nDeploy.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOS_SKILLS_DIR", catalogue)
	resolved, err := spec.LoadSkill(skillDir)
	if err != nil {
		t.Fatalf("LoadSkill: %v", err)
	}
	digest, _, err := spec.BuildSkillBundle(resolved)
	if err != nil {
		t.Fatalf("BuildSkillBundle: %v", err)
	}
	postedSkill := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/skills/telos/k8s-deploy/versions/1.0.0":
			json.NewEncoder(w).Encode(map[string]any{
				"scope":      "telos",
				"name":       "k8s-deploy",
				"version":    "1.0.0",
				"ref":        "@telos/k8s-deploy:1.0.0",
				"digest":     digest,
				"file_count": 1,
				"source_ref": "@telos/k8s-deploy:1.0.0",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/skills":
			postedSkill = true
			http.Error(w, "unexpected skill publish", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	refs, err := pushPackageSkills(
		cloud.NewClient(srv.URL, "test-token"),
		&spec.CompiledEnvironment{Skills: []*spec.Skill{{
			Name:         resolved.Name,
			Description:  resolved.Description,
			Instructions: resolved.Instructions,
			Path:         resolved.Path,
			SourceRef:    "@telos/k8s-deploy:1.0.0",
			Tags:         resolved.Tags,
			Scripts:      resolved.Scripts,
		}}},
		"user-abc",
	)
	if err != nil {
		t.Fatalf("pushPackageSkills: %v", err)
	}
	if postedSkill {
		t.Fatal("platform catalogue skill should not be republished")
	}
	if refs["k8s-deploy"] != "@telos/k8s-deploy:1.0.0" {
		t.Fatalf("refs: got %#v", refs)
	}
}

func TestLaunchSpecPlatformDoesNotResolveSkills(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: 0.1.0\nname: hosted\nplatform: cloud\nskills:\n  - server-side-only\n---\n# Hosted\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	platform, err := launchSpecPlatform(specPath)
	if err != nil {
		t.Fatalf("launchSpecPlatform: %v", err)
	}
	if platform != "cloud" {
		t.Fatalf("platform: got %q", platform)
	}
}

func TestNormalizePackageVersion(t *testing.T) {
	for input, want := range map[string]string{
		"1":                "1.0.0",
		"1.2":              "1.2.0",
		"1.2.3":            "1.2.3",
		"1.2.3-alpha.1":    "1.2.3-alpha.1",
		"1.2.3+sha.abcdef": "1.2.3+sha.abcdef",
		"1.2.3-rc.1+build": "1.2.3-rc.1+build",
	} {
		got, err := normalizePackageVersion(input)
		if err != nil {
			t.Fatalf("normalizePackageVersion(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizePackageVersion(%q): got %q want %q", input, got, want)
		}
	}
	for _, input := range []string{"v1", "1.2.3.4", "1.x", "01.2.3", "1.02.3", ""} {
		if _, err := normalizePackageVersion(input); err == nil {
			t.Fatalf("normalizePackageVersion(%q): expected error", input)
		}
	}
}

func TestApplyCloudSessionPackageCreates(t *testing.T) {
	var created bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/deployments":
			created = true
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["name"] != "auth" || body["package_ref"] != "@user-abc/auth:0.1.0" {
				t.Fatalf("body: got %#v", body)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"id":             "sess_123",
				"name":           "auth",
				"state":          "provisioning",
				"package_ref":    "@user-abc/auth:0.1.0",
				"package_digest": "sha256:new",
				"created_at":     "now",
				"updated_at":     "now",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	operation, session, err := applyCloudSessionPackage(
		cloud.NewClient(srv.URL, "test-token"),
		"auth",
		"@user-abc/auth:0.1.0",
		"",
		sessionRuntimeConfig{},
	)
	if err != nil {
		t.Fatalf("applyCloudSessionPackage: %v", err)
	}
	if operation != "created" || !created {
		t.Fatalf("operation=%q created=%v", operation, created)
	}
	if session.ID != "sess_123" {
		t.Fatalf("session: got %+v", session)
	}
}

func TestApplyCloudSessionPackageUpdatesExplicitSession(t *testing.T) {
	var updated bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/deployments/sess_123":
			updated = true
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["package_ref"] != "@user-abc/auth:0.1.1" {
				t.Fatalf("body: got %#v", body)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"id":             "sess_123",
				"name":           "auth",
				"state":          "deploying",
				"package_ref":    "@user-abc/auth:0.1.1",
				"package_digest": "sha256:new",
				"created_at":     "then",
				"updated_at":     "now",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	operation, session, err := applyCloudSessionPackage(
		cloud.NewClient(srv.URL, "test-token"),
		"auth",
		"@user-abc/auth:0.1.1",
		"sess_123",
		sessionRuntimeConfig{},
	)
	if err != nil {
		t.Fatalf("applyCloudSessionPackage: %v", err)
	}
	if operation != "updated" || !updated {
		t.Fatalf("operation=%q updated=%v", operation, updated)
	}
	if session.ID != "sess_123" {
		t.Fatalf("session: got %+v", session)
	}
}

func TestApplyCloudSessionPackageConflictAlreadyCurrent(t *testing.T) {
	var updateCalls int
	var getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/deployments/sess_123":
			updateCalls++
			http.Error(w, "conflict", http.StatusConflict)
		case r.Method == http.MethodGet && r.URL.Path == "/api/deployments/sess_123":
			getCalls++
			json.NewEncoder(w).Encode(map[string]any{
				"id":             "sess_123",
				"name":           "auth",
				"state":          "healthy",
				"package_ref":    "@user-abc/auth:0.1.1",
				"package_digest": "sha256:new",
				"created_at":     "then",
				"updated_at":     "now",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	operation, session, err := applyCloudSessionPackage(
		cloud.NewClient(srv.URL, "test-token"),
		"auth",
		"@user-abc/auth:0.1.1",
		"sess_123",
		sessionRuntimeConfig{},
	)
	if err != nil {
		t.Fatalf("applyCloudSessionPackage: %v", err)
	}
	if operation != "unchanged" {
		t.Fatalf("operation: got %q want unchanged", operation)
	}
	if session.ID != "sess_123" || session.PackageRef != "@user-abc/auth:0.1.1" {
		t.Fatalf("session: got %+v", session)
	}
	if updateCalls != 1 || getCalls != 1 {
		t.Fatalf("calls: update=%d get=%d", updateCalls, getCalls)
	}
}

func TestRootSessionContextUsesScopedToken(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	t.Setenv("TELOS_API_TOKEN", "session-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_API_ENDPOINT", "http://telos-api.local:8000")

	ctx, ok := rootSessionContext()
	if !ok {
		t.Fatal("expected root context")
	}
	if ctx.endpoint != "http://telos-api.local:8000" {
		t.Fatalf("endpoint: got %q", ctx.endpoint)
	}
	if ctx.token != "session-token" {
		t.Fatalf("token: got %q", ctx.token)
	}
	if ctx.sessionID != "sess_parent" {
		t.Fatalf("session id: got %q", ctx.sessionID)
	}
}

func TestRootSessionContextDefaultsToLocalAPI(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	t.Setenv("TELOS_API_TOKEN", "session-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")

	ctx, ok := rootSessionContext()
	if !ok {
		t.Fatal("expected root context")
	}
	if ctx.endpoint != "http://127.0.0.1:8000" {
		t.Fatalf("endpoint: got %q", ctx.endpoint)
	}
}

func TestRootSessionContextIgnoresLocalRuntime(t *testing.T) {
	t.Setenv("TELOS_API_TOKEN", "session-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))
	t.Setenv("TELOS_API_ENDPOINT", "http://telos-api.local:8000")

	if ctx, ok := rootSessionContext(); ok {
		t.Fatalf("local runtime should not be cloud root context: %#v", ctx)
	}
}

func TestLocalRootSessionIDUsesLocalSessionContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: 0.1.0\nname: local-root\nplatform: local\n---\n# Local Root\n"
	kind := sessionapi.KindController
	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		SessionKind:  &kind,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))

	sessionID, ok := localRootSessionID()
	if !ok {
		t.Fatal("expected local root session context")
	}
	if sessionID != session.SessionID {
		t.Fatalf("session id: got %q", sessionID)
	}
}

func TestLocalRootSessionIDIgnoresTaskSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: 0.1.0\nname: local-task\nplatform: local\n---\n# Local Task\n"
	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_RUNTIME", string(sessionapi.RuntimeLocal))

	if sessionID, ok := localRootSessionID(); ok {
		t.Fatalf("task session should not be local root context: %s", sessionID)
	}
}

func TestLocalRootSessionIDRequiresLocalRuntimeMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: 0.1.0\nname: local-root\nplatform: local\n---\n# Local Root\n"
	kind := sessionapi.KindController
	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		SessionKind:  &kind,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Setenv("TELOS_RUNTIME", "")
	t.Setenv("TELOS_SESSION_ID", session.SessionID)
	t.Setenv("TELOS_SESSION_DIR", root)

	if sessionID, ok := localRootSessionID(); ok {
		t.Fatalf("session should not be local root context without runtime marker: %s", sessionID)
	}
}

func TestFollowTranscriptWaitsForTranscript(t *testing.T) {
	root := t.TempDir()
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", root)
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: 0.1.0\nname: follow-test\nplatform: local\n---\n# Follow\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var out bytes.Buffer
	slept := false
	err = followTranscript(session.SessionID, &out, func(time.Duration) {
		if slept {
			t.Fatal("unexpected second sleep")
		}
		slept = true
		path := session.Specs[0].TranscriptPath
		if path == nil || *path == "" {
			t.Fatal("missing transcript path")
		}
		if err := os.MkdirAll(filepath.Dir(*path), 0o755); err != nil {
			t.Fatalf("mkdir transcript dir: %v", err)
		}
		if err := os.WriteFile(*path, []byte("# Transcript\n<progress_update>ready</progress_update>\n"), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		if _, err := store.Stop(session.SessionID); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}, false)
	if err != nil {
		t.Fatalf("followTranscript: %v", err)
	}
	if !slept {
		t.Fatal("expected follow to wait for transcript creation")
	}
	if got := out.String(); !strings.Contains(got, "ready") {
		t.Fatalf("output: got %q", got)
	}
}

func TestLocalStoreProjectsSpecUpdates(t *testing.T) {
	root := t.TempDir()
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", root)
	s := store()
	markdown := "---\nversion: 0.1.0\nname: local-update\nplatform: local\n---\n# Local Update\n"
	kind := sessionapi.KindController
	session, err := s.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown, SessionKind: &kind})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated := "---\nversion: 0.1.1\nname: local-update\nplatform: local\ninterval: 6h\n---\n# Local Update v2\n"

	if _, err := s.UpdateSpecByID(session.SessionID, sessionapi.SessionSpecUpdateRequest{SpecMarkdown: updated}); err != nil {
		t.Fatalf("UpdateSpecByID: %v", err)
	}

	transcript, err := s.Transcript(session.SessionID)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	for _, want := range []string{
		"## External Update",
		"<external_update>",
		"from version 1 to 2",
		"Current immutable spec path: `",
		"Active spec path: `",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
	events, err := s.Events(session.SessionID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var found bool
	for _, event := range events {
		if event.Event == "external_update" {
			found = true
			if got := event.Data["current_spec_version"]; got != float64(2) {
				t.Fatalf("current_spec_version: got %#v", got)
			}
		}
	}
	if !found {
		t.Fatalf("missing external_update event: %#v", events)
	}
}

func TestLocalStoreUpdatesCompletedController(t *testing.T) {
	root := t.TempDir()
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", root)
	s := store()
	markdown := "---\nversion: 0.1.0\nname: completed-update\nplatform: local\n---\n# Completed Update\n"
	kind := sessionapi.KindController
	session, err := s.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown, SessionKind: &kind})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	finished := "2026-07-08T00:00:00Z"
	completed := "completed"
	if _, err := sessionapi.MutateManifest(filepath.Join(root, session.SessionID, "session.json"), func(m *sessionapi.Manifest) error {
		m.Runner = nil
		m.Epochs = []sessionapi.Epoch{{
			ID:         1,
			StartedAt:  "2026-07-08T00:00:00Z",
			FinishedAt: &finished,
			Result:     &completed,
		}}
		return nil
	}); err != nil {
		t.Fatalf("MutateManifest: %v", err)
	}
	current, err := s.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.Status != sessionapi.StatusCompleted {
		t.Fatalf("status: got %s", current.Status)
	}
	updated := "---\nversion: 0.1.1\nname: completed-update\nplatform: local\ninterval: 6h\n---\n# Completed Update v2\n"
	response, err := s.UpdateSpecByID(session.SessionID, sessionapi.SessionSpecUpdateRequest{SpecMarkdown: updated})
	if err != nil {
		t.Fatalf("UpdateSpecByID: %v", err)
	}
	if response.Operation != "updated" {
		t.Fatalf("operation: got %q", response.Operation)
	}
}

func TestFollowTranscriptErrorsWhenTerminalWithoutTranscript(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: 0.1.0\nname: missing-transcript\nplatform: local\n---\n# Missing\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Stop(session.SessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	var out bytes.Buffer
	err = followTranscript(session.SessionID, &out, func(time.Duration) {
		t.Fatal("terminal session should not sleep")
	}, false)
	if err == nil {
		t.Fatal("expected missing terminal transcript to fail")
	}
	if !strings.Contains(err.Error(), "transcript") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFollowTranscriptSurfacesRootTranscriptError(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/sess_running/transcript":
			http.Error(w, `{"detail":"transcript backend failed"}`, http.StatusInternalServerError)
		case "/api/sessions/sess_running":
			json.NewEncoder(w).Encode(map[string]any{
				"session_id": "sess_running",
				"runtime":    "cloud",
				"status":     "running",
				"config":     map[string]any{},
				"provenance": map[string]any{},
				"specs":      []any{},
				"epochs":     []any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer cluster.Close()
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_API_TOKEN", "scoped-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_API_ENDPOINT", cluster.URL)

	var out bytes.Buffer
	err := followTranscript("sess_running", &out, func(time.Duration) {
		t.Fatal("500 transcript errors should not sleep")
	}, false)
	if err == nil {
		t.Fatal("expected transcript error")
	}
	if !strings.Contains(err.Error(), "root transcript lookup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrintLogsDefaultsToProtocolBlocks(t *testing.T) {
	transcript := `# Transcript

	hidden raw content with inline code ` + "`<progress_update>`" + `

<progress_update>First checkpoint</progress_update>

	more raw content with inline code ` + "`</progress_update>`" + `

	<progress_update ts="2026-05-20T00:00:00Z">Second checkpoint</progress_update>

	<review>
criteria,score
Correctness,8.0/10
</review>

	<summary>Needs one more check.</summary>`
	transcript += `

	<external_update>
Reload the current spec.
- Current spec version: ` + "`2`" + `
</external_update>`

	var out bytes.Buffer
	printLogs(&out, transcript, false)
	text := out.String()
	for _, want := range []string{
		"#1 First checkpoint",
		"#2 Second checkpoint",
		"Review\ncriteria,score\nCorrectness,8.0/10",
		"Summary\nNeeds one more check.",
		"External update\nReload the current spec.\n- Current spec version: `2`",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "hidden raw content") || strings.Contains(text, "more raw content") {
		t.Fatalf("log output leaked raw transcript:\n%s", text)
	}
}

func TestPrintLogsSuppressesDuplicateProtocolBlocks(t *testing.T) {
	transcript := `# Transcript

<progress_update>Checked live state</progress_update>

## Implementation 1

The agent repeated its machine-readable update:

<progress_update>Checked live state</progress_update>

<external_update>
Reload the current spec.
</external_update>

## Implementation 2

<external_update>
Reload the current spec.
</external_update>
`

	var out bytes.Buffer
	printLogs(&out, transcript, false)
	text := out.String()
	if strings.Count(text, "Checked live state") != 1 {
		t.Fatalf("expected one progress update, got:\n%s", text)
	}
	if strings.Count(text, "External update") != 1 {
		t.Fatalf("expected one external update, got:\n%s", text)
	}
}

func TestPrintLogsVerboseShowsTranscript(t *testing.T) {
	transcript := "# Transcript\nraw content\n<progress_update>Progress</progress_update>\n"

	var out bytes.Buffer
	printLogs(&out, transcript, true)
	if out.String() != transcript {
		t.Fatalf("verbose output mismatch:\n%s", out.String())
	}
}

func TestPrintCloudSessionLogsDefaultsToAgentProgress(t *testing.T) {
	events := []sessionapi.SessionEvent{
		{Event: "agent_progress", Data: map[string]any{"kind": "progress_update", "text": "ready"}},
		{Event: "agent_progress", Data: map[string]any{"kind": "review", "text": "criteria,score\nCorrectness,8/10"}},
		{Event: "agent_complete", Data: map[string]any{"status": "CONTINUE", "model": "test-model", "num_turns": float64(12)}},
		{Event: "agent_failure_recoverable", Data: map[string]any{"error": "agent_no_output", "consecutive_failures": float64(1), "max_failures": float64(3)}},
		{Event: "runtime.prepare.started", Data: map[string]any{"message": "preparing runtime", "stage": "prepare"}},
		{Event: "game_end", Data: map[string]any{"game_result": "failure", "error": "run_duration_exhausted: exceeded 1800 seconds"}},
	}

	var out bytes.Buffer
	printCloudSessionLogEvents(&out, events, false)
	text := out.String()
	for _, want := range []string{
		"#1 ready",
		"Review\ncriteria,score\nCorrectness,8/10",
		"Agent complete: CONTINUE model=test-model turns=12",
		"Recoverable failure: agent_no_output (1/3)",
		"preparing runtime",
		"Completed: failure (run_duration_exhausted: exceeded 1800 seconds)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cloud session logs missing %q:\n%s", want, text)
		}
	}
}

func TestPrintCloudSessionLogsVerboseShowsJSONEvents(t *testing.T) {
	ts := "2026-07-01T00:00:00Z"
	events := []sessionapi.SessionEvent{
		{Event: "agent_progress", Timestamp: &ts, Data: map[string]any{"kind": "progress_update", "text": "ready"}},
	}

	var out bytes.Buffer
	printCloudSessionLogEvents(&out, events, true)
	text := out.String()
	for _, want := range []string{`"event":"agent_progress"`, `"ts":"2026-07-01T00:00:00Z"`, `"text":"ready"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("verbose cloud session logs missing %q:\n%s", want, text)
		}
	}
}

func TestFollowCloudSessionLogsStreamsEvents(t *testing.T) {
	var logCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/deployments/sess_123/logs":
			logCalls++
			if r.Header.Get("Accept") != "text/event-stream" {
				t.Fatalf("Accept: got %q", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"event\":\"agent_progress\",\"data\":{\"kind\":\"progress_update\",\"text\":\"ready\"}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	err := streamCloudSessionLogs(
		cloud.NewClient(srv.URL, "test-token"),
		"sess_123",
		&out,
		func(time.Duration) {},
		false,
	)
	if err != nil {
		t.Fatalf("streamCloudSessionLogs: %v", err)
	}
	if logCalls != 1 {
		t.Fatalf("calls: logs=%d", logCalls)
	}
	if !strings.Contains(out.String(), "#1 ready") {
		t.Fatalf("follow output missing progress:\n%s", out.String())
	}
}

func TestRootLookupReturnsClusterAPIError(t *testing.T) {
	t.Setenv("TELOS_RUNTIME", "")
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/sess_root" {
			http.Error(w, `{"detail":"cluster unavailable"}`, http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer cluster.Close()
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))
	t.Setenv("TELOS_API_TOKEN", "scoped-token")
	t.Setenv("TELOS_SESSION_ID", "sess_parent")
	t.Setenv("TELOS_API_ENDPOINT", cluster.URL)

	_, err := getSessionFromAnywhere("sess_root")
	if err == nil {
		t.Fatal("expected root lookup to fail")
	}
	if !strings.Contains(err.Error(), "root session lookup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "session sess_root: not found") {
		t.Fatalf("root error fell through to generic not found: %v", err)
	}
}

func TestLocalSessionNotFoundErrorExplainsWorkspaceScope(t *testing.T) {
	configureLocalOnlyTest(t)
	t.Setenv("TELOS_SESSION_DIR", filepath.Join(t.TempDir(), "sessions"))

	_, err := getSessionFromAnywhere("local_missing")
	if err == nil {
		t.Fatal("expected missing local session")
	}
	text := err.Error()
	for _, want := range []string{
		"session local_missing not found in",
		"Local sessions are workspace-scoped",
		"TELOS_SESSION_DIR=/path/to/.telos/sessions",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing guidance %q:\n%s", want, text)
		}
	}
}

func TestLocalSessionRootDefaultsToOutputRoot(t *testing.T) {
	t.Setenv("TELOS_SESSION_DIR", "")
	outputRoot := filepath.Join(t.TempDir(), "telos-output")
	t.Setenv("TELOS_OUTPUT_ROOT", outputRoot)

	got := localSessionRoot()
	prefix := outputRoot + string(os.PathSeparator) + "execroot" + string(os.PathSeparator)
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("local session root %q should be under %q", got, prefix)
	}
	if !strings.HasSuffix(got, string(os.PathSeparator)+"sessions") {
		t.Fatalf("local session root %q should end with sessions", got)
	}
}

func TestLocalSessionRootHonorsSessionDirEnv(t *testing.T) {
	want := filepath.Join(t.TempDir(), "sessions")
	t.Setenv("TELOS_SESSION_DIR", want)
	t.Setenv("TELOS_OUTPUT_ROOT", filepath.Join(t.TempDir(), "telos-output"))

	got := localSessionRoot()
	if got != want {
		t.Fatalf("local session root: got %q want %q", got, want)
	}
}

func configureCloudTest(t *testing.T, endpoint string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", endpoint)
	t.Setenv("TELOS_AUTH_TOKEN", "control-token")
}

func configureLocalOnlyTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TELOS_CONFIG", filepath.Join(dir, "config.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", "")
	t.Setenv("TELOS_AUTH_TOKEN", "")
}
