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
	"github.com/telos-org/telos/internal/sessionapi"
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
	printPlanPreview(&out, compiled, "./SPEC.md", "cloud", "personal", comparison)
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
	printPlanPreview(&out, compiled, "./SPEC.md", "cloud", "personal", comparison)
	if !strings.Contains(out.String(), "No spec changes.") {
		t.Fatalf("plan output:\n%s", out.String())
	}
}

func TestPlanSessionJSONReportsUpdateWithoutCreate(t *testing.T) {
	configurePlanVerifierSkills(t)
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
		Spec    map[string]any `json:"spec"`
		Session map[string]any `json:"session"`
		Target  struct {
			WillCreate bool `json:"will_create_session"`
			WillUpdate bool `json:"will_update_session"`
		} `json:"target"`
		Change struct {
			Current  planSpecState `json:"current"`
			Proposed planSpecState `json:"proposed"`
		} `json:"change"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Target.WillCreate || !plan.Target.WillUpdate {
		t.Fatalf("target: got %+v", plan.Target)
	}
	if _, ok := plan.Spec["lineage"]; ok {
		t.Fatalf("spec lineage should be omitted: %#v", plan.Spec)
	}
	if _, ok := plan.Session["lineage"]; ok {
		t.Fatalf("session lineage should be omitted: %#v", plan.Session)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	target := raw["target"].(map[string]any)
	if _, ok := target["will_mutate"]; ok {
		t.Fatalf("will_mutate should be omitted: %#v", target)
	}
	if plan.Change.Current.Version != "1.2.3" || plan.Change.Proposed.Version != "1.2.3" {
		t.Fatalf("versions: current=%q proposed=%q", plan.Change.Current.Version, plan.Change.Proposed.Version)
	}
	if len(plan.Change.Current.Skills) == 0 || len(plan.Change.Proposed.Skills) == 0 {
		t.Fatalf("skill locks missing: current=%#v proposed=%#v", plan.Change.Current.Skills, plan.Change.Proposed.Skills)
	}
	if plan.Change.Current.Skills[0].Digest == "" || plan.Change.Proposed.Skills[0].Digest == "" {
		t.Fatalf("skill lock digest missing: current=%#v proposed=%#v", plan.Change.Current.Skills, plan.Change.Proposed.Skills)
	}
}

func configurePlanVerifierSkills(t *testing.T) string {
	t.Helper()
	catalogue := t.TempDir()
	for _, name := range []string{"verify-engineering", "verify-quality"} {
		dir := filepath.Join(catalogue, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := []byte("---\nname: " + name + "\n---\nVerify.\n")
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TELOS_SKILLS_DIR", catalogue)
	return catalogue
}

func TestCompareLocalSessionSpecUsesPersistedSkillLocks(t *testing.T) {
	catalogue := configurePlanVerifierSkills(t)
	skillDir := filepath.Join(catalogue, "build-dashboard")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: build-dashboard\n---\nBuild the dashboard.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	markdown := []byte(`---
name: local-plan
version: 1.0.0
platform: local
skills:
  - build-dashboard*
---

# Goal

Build it.
`)
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	kind := sessionapi.KindController
	localStore := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdownText := string(markdown)
	session, err := localStore.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdownText,
		SessionKind:  &kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, markdown, 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := planSpecStateForCompiled(compiled, markdown)
	if err != nil {
		t.Fatal(err)
	}

	comparison, err := compareSessionSpec(
		session.SessionID,
		markdown,
		proposed,
		"local",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.current.Skills) != len(comparison.proposed.Skills) {
		t.Fatalf("skill delta: current=%#v proposed=%#v", comparison.current.Skills, comparison.proposed.Skills)
	}
	for i := range comparison.current.Skills {
		if comparison.current.Skills[i] != comparison.proposed.Skills[i] {
			t.Fatalf("skill delta: current=%#v proposed=%#v", comparison.current.Skills, comparison.proposed.Skills)
		}
	}
	if len(comparison.current.Rubrics) != 1 || comparison.current.Rubrics[0] != "build-dashboard" {
		t.Fatalf("current rubrics = %#v", comparison.current.Rubrics)
	}
}

func TestPlanSpecStateCapturesIntervalSkillLocksAndRubrics(t *testing.T) {
	currentIntervalSpec := []byte("---\nname: demo\nversion: 1.0.0\ninterval: 5m\n---\n\n# Goal\n\nCurrent.\n")
	proposedIntervalSpec := []byte("---\nname: demo\nversion: 1.1.0\ninterval: 10m\n---\n\n# Goal\n\nProposed.\n")
	currentManifest := &spec.ApplyPackageManifest{Skills: map[string]spec.ApplyPackageSkillLock{
		"verify-quality": {
			Ref:    "@telos/verify-quality:1.0.0",
			Digest: "sha256:current",
		},
	}}
	proposedManifest := &spec.ApplyPackageManifest{Skills: map[string]spec.ApplyPackageSkillLock{
		"verify-quality": {
			Ref:     "@telos/verify-quality:1.1.0",
			Digest:  "sha256:proposed",
			Starred: true,
		},
	}}

	current, err := planSpecStateFromMarkdown(currentIntervalSpec, currentManifest)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := planSpecStateFromMarkdown(proposedIntervalSpec, proposedManifest)
	if err != nil {
		t.Fatal(err)
	}
	if current.IntervalSeconds == nil || *current.IntervalSeconds != 300 {
		t.Fatalf("current interval = %#v", current.IntervalSeconds)
	}
	if proposed.IntervalSeconds == nil || *proposed.IntervalSeconds != 600 {
		t.Fatalf("proposed interval = %#v", proposed.IntervalSeconds)
	}
	if len(proposed.Rubrics) != 1 || proposed.Rubrics[0] != "verify-quality" {
		t.Fatalf("proposed rubrics = %#v", proposed.Rubrics)
	}

	var out bytes.Buffer
	printPlanStateDelta(&out, current, proposed)
	normalized := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"Version 1.0.0 -> 1.1.0",
		"Interval 5m0s -> 10m0s",
		"verify-quality @telos/verify-quality:1.0.0 sha256:current",
		"verify-quality @telos/verify-quality:1.1.0 sha256:proposed *",
		"rubrics  - -> verify-quality",
	} {
		want = strings.Join(strings.Fields(want), " ")
		if !strings.Contains(normalized, want) {
			t.Fatalf("plan delta missing %q:\n%s", want, out.String())
		}
	}
}
