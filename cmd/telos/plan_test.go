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
	"github.com/telos-org/telos/internal/config"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

func TestPlanShowsAccessChangesIncludingRemovals(t *testing.T) {
	current, err := planSpecStateFromMarkdown([]byte("---\nname: test\nversion: 1.0.0\nintegrations: [sec_stripe]\nallowlist: [{host: api.stripe.com, methods: [POST], paths: ['/v1/*']}]\n---\nBody"), nil)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := planSpecStateFromMarkdown([]byte("---\nname: test\nversion: 1.0.0\nintegrations: []\nallowlist: []\n---\nBody"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printPlanStateDelta(&out, current, proposed)
	for _, expected := range []string{"Access", "sec_stripe", "api.stripe.com", "POST", "/v1/*", "integrations: none; allowlist: none"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("access delta omitted %q: %s", expected, out.String())
		}
	}
	encoded, _ := json.Marshal(proposed)
	if !strings.Contains(string(encoded), `"access":{"integrations":[],"allowlist":[]}`) {
		t.Fatalf("JSON plan omitted explicit removal: %s", encoded)
	}
}

func TestPlanDistinguishesIntegrationLabelsFromPermissionChanges(t *testing.T) {
	current := planSpecState{Access: &spec.AccessSpec{
		Integrations: []string{"sec_stripe"}, Allowlist: []spec.NetworkRule{},
	}}
	proposed := planSpecState{Access: &spec.AccessSpec{
		Integrations: []string{"sec_stripe"}, Allowlist: []spec.NetworkRule{},
		IntegrationNames: map[string]string{"sec_stripe": "Stripe production"},
	}}
	var out bytes.Buffer
	printPlanStateDelta(&out, current, proposed)
	if !strings.Contains(out.String(), "Integration labels (permissions unchanged)") || !strings.Contains(out.String(), `"Stripe production" (sec_stripe)`) {
		t.Fatalf("label-only change presented incorrectly: %s", out.String())
	}
	out.Reset()
	proposed.Access.Integrations = []string{"sec_other"}
	proposed.Access.IntegrationNames = map[string]string{"sec_other": "Stripe production"}
	printPlanStateDelta(&out, current, proposed)
	if !strings.Contains(out.String(), "Access") || strings.Contains(out.String(), "permissions unchanged") {
		t.Fatalf("same label hid an ID change: %s", out.String())
	}
}

func TestPlanShowsHostCredentialBindingsDefaultsAndChanges(t *testing.T) {
	parse := func(fields string) planSpecState {
		t.Helper()
		state, err := planSpecStateFromMarkdown([]byte("---\nname: test\nversion: 1.0.0\n"+fields+"---\nBody"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	current := parse("network: [{host: api.example.com, credentials: sec-one}, {host: public.example.com}]\n")
	legacy := parse("egress: [{host: api.example.com, credentials: sec-one}, {host: public.example.com}]\n")
	if !samePlanPermissions(current.Access, legacy.Access) {
		t.Fatal("renaming egress to network must not change permissions")
	}
	proposed := parse("network: [{host: api.example.com, credentials: sec-two}, {host: public.example.com}]\n")
	var out bytes.Buffer
	printPlanStateDelta(&out, current, proposed)
	for _, expected := range []string{
		"Access", "api.example.com (credentials: sec-one; methods: all; paths: *)",
		"api.example.com (credentials: sec-two; methods: all; paths: *)",
		"public.example.com (credentials: none; methods: all; paths: *)",
	} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("missing binding/default %q: %s", expected, out.String())
		}
	}
	if strings.Contains(out.String(), "permissions unchanged") || samePlanPermissions(current.Access, proposed.Access) {
		t.Fatalf("credential-only change was treated as a label edit: %s", out.String())
	}
	out.Reset()
	printPlanPreview(&out, &spec.CompiledEnvironment{Environment: &spec.EnvironmentSpec{
		Name: "test", Access: proposed.Access,
	}}, "SPEC.md", "cloud", "personal", nil)
	if !strings.Contains(out.String(), "api.example.com (credentials: sec-two; methods: all; paths: *)") {
		t.Fatalf("initial preview hid access binding: %s", out.String())
	}
	out.Reset()
	proposed = parse("network: []\n")
	printPlanStateDelta(&out, current, proposed)
	if !strings.Contains(out.String(), "network: none") {
		t.Fatalf("missing explicit revocation: %s", out.String())
	}
	encoded, _ := json.Marshal(proposed)
	if !strings.Contains(string(encoded), `"access":{"network":[]}`) || strings.Contains(string(encoded), "allowlist") {
		t.Fatalf("new plan schema lost explicit revocation or emitted legacy fields: %s", encoded)
	}
	proposed = parse("network: [{host: api.example.com, credentials: sec-one, methods: [GET], paths: ['/reports/*']}]\n")
	if got := formatPlanAccess(proposed.Access); !strings.Contains(got, "credentials: sec-one; methods: GET; paths: /reports/*") {
		t.Fatalf("restricted binding missing: %s", got)
	}
}

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

func TestPlanShowsCanonicalContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/account/bootstrap" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"personal_org_id": "org_personal",
			"organizations": []map[string]any{
				{
					"id":           "org_personal",
					"handle":       "grohan",
					"display_name": "Rohan",
					"kind":         "personal",
					"role":         "owner",
				},
				{
					"id":           "org_telos",
					"handle":       "telos",
					"display_name": "Telos",
					"kind":         "platform",
					"role":         "owner",
				},
			},
		})
	}))
	defer srv.Close()

	configureCloudTest(t, srv.URL)
	t.Setenv(config.ContextEnv, "")
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, []byte(`---
name: demo
version: 1.0.0
platform: cloud
---

# Goal

Serve a demo.
`), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name   string
		stored string
		want   string
		hidden string
	}{
		{name: "team", stored: "org_telos", want: "@telos", hidden: "org_telos"},
		{name: "personal", stored: "org_personal", want: "personal", hidden: "org_personal"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := config.SaveConfig(&config.Config{Context: tt.stored}); err != nil {
				t.Fatal(err)
			}
			out := captureStdout(t, func() {
				cmdPlan([]string{specPath})
			})
			if got := configOutputValue(t, out, "Context"); got != tt.want {
				t.Fatalf("Context = %q, want %q\n%s", got, tt.want, out)
			}
			if strings.Contains(out, tt.hidden) {
				t.Fatalf("plan exposed internal organization ID:\n%s", out)
			}
		})
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

func TestPlanEgressOrderingDoesNotChangePermissions(t *testing.T) {
	read := func(rules string) planSpecState {
		t.Helper()
		state, err := planSpecStateFromMarkdown([]byte("---\nname: demo\nversion: 1.0.0\nnetwork:\n"+rules+"---\n"), nil)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	current := read(`  - host: public.example.com
  - host: api.example.com
    credentials: sec-example
    methods: [POST, GET]
    paths: [/z/*, /a/*]
`)
	proposed := read(`  - host: api.example.com
    credentials: sec-example
    methods: [GET, POST]
    paths: [/a/*, /z/*]
  - host: public.example.com
  - host: api.example.com
    credentials: sec-example
    methods: [POST, GET]
    paths: [/z/*, /a/*]
`)
	if !samePlanPermissions(current.Access, proposed.Access) {
		t.Fatal("ordering and repeated equivalent rows changed permissions")
	}
	var out bytes.Buffer
	printPlanStateDelta(&out, current, proposed)
	if out.Len() != 0 {
		t.Fatalf("equivalent access should not produce an access delta: %s", out.String())
	}
	if current.Access.Network[1].Methods[0] != "POST" || current.Access.Network[1].Paths[0] != "/z/*" {
		t.Fatal("formatting mutated the authored rule order")
	}
}
