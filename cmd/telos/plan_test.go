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
			Operation string `json:"operation"`
		} `json:"target"`
		Change struct {
			Current  planSpecState `json:"current"`
			Proposed planSpecState `json:"proposed"`
		} `json:"change"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Target.Operation != "update" {
		t.Fatalf("target: got %+v", plan.Target)
	}
	if _, ok := plan.Spec["lineage"]; ok {
		t.Fatalf("spec lineage should be omitted: %#v", plan.Spec)
	}
	if _, ok := plan.Session["lineage"]; ok {
		t.Fatalf("session lineage should be omitted: %#v", plan.Session)
	}
	if _, ok := plan.Spec["required_verifier_skills"]; ok {
		t.Fatalf("plan should not expose internal verifier roles: %#v", plan.Spec)
	}
	if _, ok := plan.Spec["required_rubrics"]; !ok {
		t.Fatalf("plan should expose required rubrics: %#v", plan.Spec)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	target := raw["target"].(map[string]any)
	for _, key := range []string{"will_mutate", "will_create_session", "will_update_session"} {
		if _, ok := target[key]; ok {
			t.Fatalf("redundant target field %q should be omitted: %#v", key, target)
		}
	}
	if _, ok := raw["user"]; ok {
		t.Fatalf("plan should omit synthetic user metadata: %#v", raw["user"])
	}
	if plan.Change.Current.Version != "1.2.3" || plan.Change.Proposed.Version != "1.2.3" {
		t.Fatalf("versions: current=%q proposed=%q", plan.Change.Current.Version, plan.Change.Proposed.Version)
	}
	if len(plan.Change.Current.Skills) != 0 || len(plan.Change.Proposed.Skills) != 0 {
		t.Fatalf("undeclared skills were injected: current=%#v proposed=%#v", plan.Change.Current.Skills, plan.Change.Proposed.Skills)
	}
}

func configurePlanSkillsCatalogue(t *testing.T) string {
	t.Helper()
	catalogue := t.TempDir()
	t.Setenv("TELOS_SKILLS_DIR", catalogue)
	return catalogue
}

func TestCompareLocalSessionSpecUsesPersistedSkillLocks(t *testing.T) {
	catalogue := configurePlanSkillsCatalogue(t)
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
	required := false
	for _, skill := range comparison.current.Skills {
		if skill.Name == "build-dashboard" && skill.Starred {
			required = true
			break
		}
	}
	if !required {
		t.Fatalf("current required skill = %#v", comparison.current.Skills)
	}
}

func TestPlanSpecStateCapturesResolvedChanges(t *testing.T) {
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
	if len(proposed.Skills) != 1 || !proposed.Skills[0].Starred {
		t.Fatalf("proposed required skill = %#v", proposed.Skills)
	}

	var out bytes.Buffer
	printPlanStateDelta(&out, current, proposed)
	normalized := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"Version 1.0.0 -> 1.1.0",
		"Interval 5m0s -> 10m0s",
		"verify-quality @telos/verify-quality:1.0.0 sha256:current",
		"verify-quality @telos/verify-quality:1.1.0 sha256:proposed *",
	} {
		want = strings.Join(strings.Fields(want), " ")
		if !strings.Contains(normalized, want) {
			t.Fatalf("plan delta missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(normalized, "rubrics") {
		t.Fatalf("starred skill locks already express required rubrics:\n%s", out.String())
	}
}

func TestPrintPlanStateDeltaOmitsUnchangedResolvedState(t *testing.T) {
	interval := 300
	state := planSpecState{
		Version:         "1.0.0",
		IntervalSeconds: &interval,
		Skills: []planSkillLock{{
			Name:    "verify-quality",
			Ref:     "@telos/verify-quality:1.0.0",
			Digest:  "sha256:same",
			Starred: true,
		}},
	}

	var out bytes.Buffer
	printPlanStateDelta(&out, state, state)
	if out.Len() != 0 {
		t.Fatalf("unchanged resolved state should be silent:\n%s", out.String())
	}
}
