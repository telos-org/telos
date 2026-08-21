package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

func TestCompilePlanSpecLeavesPersistentRegistryCacheUntouched(t *testing.T) {
	digest, bundle := testRegistrySkillBundle(t, "verify-test")
	metadataRequests := 0
	bundleRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
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
	t.Setenv("TELOS_API_ENDPOINT", srv.URL)
	t.Setenv("TELOS_AUTH_TOKEN", "test-token")
	t.Setenv(spec.RegistrySkillsDirEnv, persistentCache)
	t.Setenv("TELOS_SKILLS_DIR", t.TempDir())
	markdown := []byte("---\nversion: 0.1.0\nname: remote-skill\nplatform: local\nskills: '@telos/verify-test:1.2.3*'\n---\nUse the remote skill.\n")
	specPath := filepath.Join(t.TempDir(), "SPEC.md")
	if err := os.WriteFile(specPath, markdown, 0o644); err != nil {
		t.Fatal(err)
	}

	compiled, state, err := compilePlanSpec(specPath, "", markdown)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Skills) != 1 || compiled.Skills[0].Instructions != "Verify the result." {
		t.Fatalf("compiled skills: %#v", compiled.Skills)
	}
	if len(state.Skills) != 1 || state.Skills[0].Digest != digest || !state.Skills[0].Starred {
		t.Fatalf("planned skill locks: %#v", state.Skills)
	}
	if got := os.Getenv(spec.RegistrySkillsDirEnv); got != persistentCache {
		t.Fatalf("registry cache environment = %q, want %q", got, persistentCache)
	}
	ref, _ := spec.ParseRegistrySkillRef("@telos/verify-test:1.2.3")
	if _, err := os.Stat(spec.RegistrySkillPath(ref)); !os.IsNotExist(err) {
		t.Fatalf("plan populated persistent registry cache: %v", err)
	}
	if metadataRequests != 1 || bundleRequests != 1 {
		t.Fatalf("registry requests: metadata=%d bundle=%d", metadataRequests, bundleRequests)
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
