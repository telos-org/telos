package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func TestPrepareRegistrySkillsCachesAndPinsExactVersion(t *testing.T) {
	digest, bundle := testRegistrySkillBundle(t, "verify-test")

	metadataRequests := 0
	bundleRequests := 0
	postedSkill := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/skills/telos/verify-test/versions/1.2.3":
			metadataRequests++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"scope":      "telos",
				"name":       "verify-test",
				"version":    "1.2.3",
				"ref":        "@telos/verify-test:1.2.3",
				"digest":     digest,
				"file_count": 1,
				"source_ref": "@telos/verify-test:1.2.3",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/skills/telos/verify-test/versions/1.2.3/bundle":
			bundleRequests++
			_, _ = w.Write(bundle)
		case r.Method == http.MethodPost && r.URL.Path == "/api/skills":
			postedSkill = true
			http.Error(w, "unexpected skill publish", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cache := t.TempDir()
	catalogue := t.TempDir()
	t.Setenv("TELOS_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", srv.URL)
	t.Setenv("TELOS_AUTH_TOKEN", "test-token")
	t.Setenv(spec.RegistrySkillsDirEnv, cache)
	t.Setenv("TELOS_SKILLS_DIR", catalogue)
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(
		specPath,
		[]byte("---\nversion: 0.1.0\nname: remote-skill\nplatform: local\nskills: '@telos/verify-test:1.2.3*'\n---\nUse the remote skill.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := prepareRegistrySkills(specPath); err != nil {
		t.Fatalf("prepareRegistrySkills: %v", err)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	if len(compiled.Skills) != 1 || compiled.Skills[0].Instructions != "Verify the result." {
		t.Fatalf("skills: got %#v", compiled.Skills)
	}
	if len(compiled.RequiredVerifierSkills) != 1 {
		t.Fatalf("required verifier skills: got %#v", compiled.RequiredVerifierSkills)
	}
	if compiled.Skills[0].SourceRef != "@telos/verify-test:1.2.3" {
		t.Fatalf("source ref: got %q", compiled.Skills[0].SourceRef)
	}

	refs, err := pushPackageSkills(
		cloud.NewClient(srv.URL, "test-token"),
		compiled,
		"user-scope",
		"",
	)
	if err != nil {
		t.Fatalf("pushPackageSkills: %v", err)
	}
	if refs["verify-test"] != "@telos/verify-test:1.2.3" {
		t.Fatalf("refs: got %#v", refs)
	}
	if postedSkill {
		t.Fatal("cached registry skill was republished")
	}
	if metadataRequests != 2 || bundleRequests != 1 {
		t.Fatalf("requests: metadata=%d bundle=%d", metadataRequests, bundleRequests)
	}

	if err := prepareRegistrySkills(specPath); err != nil {
		t.Fatalf("prepareRegistrySkills cached: %v", err)
	}
	if metadataRequests != 2 || bundleRequests != 1 {
		t.Fatalf("cached prepare made network requests: metadata=%d bundle=%d", metadataRequests, bundleRequests)
	}
}

func TestPlanStagesRegistrySkillsWithoutPopulatingPersistentCache(t *testing.T) {
	digest, bundle := testRegistrySkillBundle(t, "verify-test")
	metadataRequests := 0
	bundleRequests := 0
	var stagedPackage []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/deployment-plans/packages":
			var payload struct {
				Data []byte `json:"data_base64"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			stagedPackage = payload.Data
			_, _ = w.Write([]byte(`{"ref":"@test/remote-skill:0.1.0"}`))
		case "/api/skills/telos/verify-test/versions/1.2.3":
			metadataRequests++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"scope":   "telos",
				"name":    "verify-test",
				"version": "1.2.3",
				"ref":     "@telos/verify-test:1.2.3",
				"digest":  digest,
			})
		case "/api/skills/telos/verify-test/versions/1.2.3/bundle":
			bundleRequests++
			_, _ = w.Write(bundle)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	persistentCache := filepath.Join(t.TempDir(), "registry-cache")
	t.Setenv("TELOS_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("TELOS_API_ENDPOINT", srv.URL)
	t.Setenv("TELOS_AUTH_TOKEN", "test-token")
	t.Setenv(spec.RegistrySkillsDirEnv, persistentCache)
	t.Setenv("TELOS_SKILLS_DIR", t.TempDir())
	markdown := []byte("---\nversion: 0.1.0\nname: remote-skill\nplatform: local\nskills: '@telos/verify-test:1.2.3*'\n---\nUse the remote skill.\n")
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, markdown, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := stageCloudPlanPackage(cloud.NewClient(srv.URL, "test-token"), cloudPlanInput{
		specArg: specPath, sessionID: "goal_existing", mode: "preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := spec.ApplyPackageSpec(stagedPackage)
	if err != nil {
		t.Fatal(err)
	}
	lock := manifest.Skills["verify-test"]
	if lock.Digest != digest || !lock.Starred {
		t.Fatalf("planned skill lock: %#v", lock)
	}
	if got := os.Getenv(spec.RegistrySkillsDirEnv); got != persistentCache {
		t.Fatalf("registry cache environment = %q, want %q", got, persistentCache)
	}
	ref, _ := spec.ParseRegistrySkillRef("@telos/verify-test:1.2.3")
	if _, err := os.Stat(spec.RegistrySkillPath(ref)); !os.IsNotExist(err) {
		t.Fatalf("plan populated persistent registry cache: %v", err)
	}
	if metadataRequests != 2 || bundleRequests != 1 {
		t.Fatalf("registry requests: metadata=%d bundle=%d", metadataRequests, bundleRequests)
	}
}

func TestPlanSkillPreparationStopsItsInFlightRequestOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/skills/telos/verify-test/versions/1.2.3" {
			t.Errorf("canceled preparation made a subsequent request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		cancel()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			t.Error("skill request ignored cancellation")
			_, _ = w.Write([]byte(`{"scope":"telos","name":"verify-test","version":"1.2.3","digest":"sha256:test"}`))
		}
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	t.Setenv(spec.RegistrySkillsDirEnv, filepath.Join(t.TempDir(), "registry-cache"))
	t.Setenv("TELOS_SKILLS_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "SPEC.md")
	markdown := "---\nname: demo\nversion: 1.0.0\nskills: '@telos/verify-test:1.2.3*'\n---\nVerify the demo.\n"
	if err := os.WriteFile(path, []byte(markdown), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := stageCloudPlanPackage(cloud.NewClient(server.URL, "token").WithContext(ctx), cloudPlanInput{
		specArg: path, sessionID: "goal_existing", mode: "preview",
	})
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("canceled preparation continued: error=%v calls=%d", err, calls.Load())
	}
}

func testRegistrySkillBundle(t *testing.T, name string) (string, []byte) {
	t.Helper()
	sourceDir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceDir, "SKILL.md"),
		[]byte("---\nname: "+name+"\n---\nVerify the result.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	skill, err := spec.LoadSkill(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	digest, bundle, err := spec.BuildSkillBundle(skill)
	if err != nil {
		t.Fatal(err)
	}
	return digest, bundle
}
