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
)

func TestDeploymentPlanPreviewShowsChangedAndUnchangedSpecs(t *testing.T) {
	for _, changed := range []bool{false, true} {
		request := testDeploymentPlan("preview", "preview")
		if !changed {
			request.Preview.ProposedSpec = request.Preview.BaseSpec
		}
		var out bytes.Buffer
		printDeploymentPlanDetails(&out, &request)
		if !changed {
			if !strings.Contains(out.String(), "No spec changes.") || strings.Contains(out.String(), "--- deployed/") {
				t.Fatalf("unchanged preview: %s", out.String())
			}
			continue
		}
		for _, want := range []string{"--- deployed/SPEC.md", "+++ proposed/SPEC.md", "-Serve a demo.", "+Serve an updated demo."} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("preview missing %q: %s", want, out.String())
			}
		}
	}
}

func TestPlanShowsCanonicalContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/account/bootstrap" && serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.URL.Path == "/api/deployment-plans" {
			_ = json.NewEncoder(w).Encode(testDeploymentPlan("preview", "preview"))
			return
		}
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
	var plans int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/deployments/sess_123":
			_, _ = w.Write([]byte(`{"id":"sess_123","current_revision_id":"rev_7"}`))
		case "/api/deployment-plans":
			plans++
			var options cloud.DeploymentPlanOptions
			_ = json.NewDecoder(r.Body).Decode(&options)
			if options.Mode != "preview" || options.Create != nil || options.Update == nil || options.Update.ExpectedCurrentRevisionID != "rev_7" {
				t.Errorf("preview=%+v", options)
			}
			_ = json.NewEncoder(w).Encode(testDeploymentPlan("preview", "preview"))
		default:
			t.Errorf("preview mutated deployment: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	path := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(path, []byte(testPlanSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { cmdPlan([]string{path, "--goal", "sess_123", "--json"}) })
	var receipt struct {
		Operation string                    `json:"operation"`
		Request   cloud.ChangeRequestRecord `json:"change_request"`
		ReviewURL string                    `json:"review_url"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Operation != "preview" || receipt.Request.Mode != "preview" || receipt.Request.Preview == nil || receipt.ReviewURL == "" || plans != 1 {
		t.Fatalf("receipt=%s plans=%d", out, plans)
	}
}

func configurePlanSkillsCatalogue(t *testing.T) string {
	t.Helper()
	catalogue := t.TempDir()
	t.Setenv("TELOS_SKILLS_DIR", catalogue)
	return catalogue
}

func TestPlanSpecStateCapturesResolvedChanges(t *testing.T) {
	currentIntervalSpec := []byte("---\nname: demo\nversion: 1.0.0\ninterval: 5m\n---\n\n# Goal\n\nCurrent.\n")
	proposedIntervalSpec := []byte("---\nname: demo\nversion: 1.1.0\ninterval: 10m\n---\n\n# Goal\n\nProposed.\n")
	current := deploymentPlanState(string(currentIntervalSpec), []cloud.DeploymentPlanSkill{
		{Name: "verify-quality", Ref: "@telos/verify-quality:1.0.0", Digest: "sha256:current"},
	})
	proposed := deploymentPlanState(string(proposedIntervalSpec), []cloud.DeploymentPlanSkill{
		{Name: "verify-quality", Ref: "@telos/verify-quality:1.1.0", Digest: "sha256:proposed", Starred: true},
	})
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
		"Interval 5m -> 10m",
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

func TestFormatPlanIntervalMatchesSpecStyle(t *testing.T) {
	for seconds, want := range map[int]string{45: "45s", 30 * 60: "30m", 6 * 60 * 60: "6h", 90 * 60: "1h30m", 60*60 + 30: "1h0m30s"} {
		if got := formatPlanInterval(&seconds); got != want {
			t.Fatalf("formatPlanInterval(%d) = %q, want %q", seconds, got, want)
		}
	}
	if got := formatPlanInterval(nil); got != "-" {
		t.Fatalf("formatPlanInterval(nil) = %q", got)
	}
}
