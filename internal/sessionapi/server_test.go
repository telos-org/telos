package sessionapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

// newTestServer returns an httptest.Server backed by a temporary FileStore.
func newTestServer(t *testing.T) (*httptest.Server, *sessionapi.FileStore) {
	t.Helper()
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, store, sessionapi.AllowAllAuthorizer{})
	return httptest.NewServer(mux), store
}

// --------- POST /api/sessions ---------------------------------------------------------------------------------------------------------------------------------------------------------------

func TestCreateSession(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := `{
		"spec_markdown": "---\nversion: v0\nname: my-task\nplatform: local\n---\n# My Task\n",
		"model": "claude-opus-4-6",
		"thinking": "medium",
		"max_cost_usd": 10.0
	}`

	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, b)
	}

	var session sessionapi.Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify the public Sessions API JSON contract.
	assertNonEmpty(t, "session_id", session.SessionID)
	assertEqual(t, "status", string(session.Status), "pending")
	assertEqual(t, "runtime", string(session.Runtime), "local")

	if session.SpecName == nil || *session.SpecName != "my-task" {
		t.Errorf("expected spec_name=my-task, got %v", session.SpecName)
	}
	if session.CreatedAt == nil || *session.CreatedAt == "" {
		t.Error("expected non-empty created_at")
	}

	// Config should reflect the request parameters.
	assertConfigStr(t, session.Config, "model", "claude-opus-4-6")
	assertConfigStr(t, session.Config, "thinking", "medium")
	assertConfigFloat(t, session.Config, "max_cost_usd", 10.0)

	// Provenance should be present (local mode).
	if session.Provenance == nil {
		t.Error("expected non-nil provenance")
	}

	// Specs array.
	if len(session.Specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(session.Specs))
	}
	spec := session.Specs[0]
	if spec.Name == nil || *spec.Name != "my-task" {
		t.Errorf("expected spec name=my-task, got %v", spec.Name)
	}
	if spec.EvidencePath == nil || *spec.EvidencePath == "" {
		t.Error("expected non-empty evidence_path")
	}

	// Empty lists should serialize as arrays, not null.
	if session.Epochs == nil {
		t.Error("epochs should be empty array, not nil")
	}
	if session.SpecVersions == nil {
		t.Error("spec_versions should be empty array, not nil")
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != "true" {
		t.Fatalf("unexpected health body: %#v", body)
	}
}

func TestCreateSessionPersistsSpecMarkdown(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: markdown-task\nplatform: local\ninterval: 30s\n---\n# Task\n\nDo it."

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if session.SpecName == nil || *session.SpecName != "markdown-task" {
		t.Fatalf("spec name: got %v", session.SpecName)
	}
	if session.SessionSpecPath == nil || *session.SessionSpecPath == "" {
		t.Fatal("expected top-level session_spec_path")
	}
	if session.Specs[0].SessionSpecPath == nil || *session.Specs[0].SessionSpecPath == "" {
		t.Fatal("expected spec session_spec_path")
	}
	if session.Specs[0].ContentHash == nil || *session.Specs[0].ContentHash == "" {
		t.Fatal("expected content hash")
	}
	if session.Specs[0].IntervalSeconds == nil || *session.Specs[0].IntervalSeconds != 30 {
		t.Fatalf("interval: got %v", session.Specs[0].IntervalSeconds)
	}
	data, err := os.ReadFile(*session.Specs[0].SessionSpecPath)
	if err != nil {
		t.Fatalf("read session spec: %v", err)
	}
	if string(data) != markdown {
		t.Fatalf("session spec was not persisted")
	}
}

func TestCreateSessionPersistsUntil(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: review-task\nplatform: local\n---\n# Review Task\n"
	until := 3

	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		Until:        &until,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	assertConfigFloat(t, session.Config, "until", 3)

	manifest, err := sessionapi.ReadManifest(filepath.Join(root, session.SessionID, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Config.Until != 3 {
		t.Fatalf("manifest until: got %d", manifest.Config.Until)
	}
	if manifest.Config.MaxRounds != sessionapi.DefaultMaxRounds || manifest.Config.MaxDurationSec != sessionapi.DefaultMaxDurationSec {
		t.Fatalf("manifest defaults: max_rounds=%d max_duration_sec=%d", manifest.Config.MaxRounds, manifest.Config.MaxDurationSec)
	}
	if got := intValueFromConfig(t, session.Config, "max_rounds"); got != sessionapi.DefaultMaxRounds {
		t.Fatalf("session max_rounds default: got %d", got)
	}
	if got := intValueFromConfig(t, session.Config, "max_duration_sec"); got != sessionapi.DefaultMaxDurationSec {
		t.Fatalf("session max_duration_sec default: got %d", got)
	}
}

func TestCreateSessionRejectsInvalidUntil(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: bad-until\nplatform: local\n---\n# Bad Until\n"
	until := 0

	_, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		Until:        &until,
	})
	if err == nil {
		t.Fatal("expected invalid until error")
	}
	if !strings.Contains(err.Error(), "until must be positive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloudCreateSessionHonorsExplicitChildTaskKind(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	markdown := "---\nversion: v0\nname: one-off\nplatform: cloud\n---\n# One Off\n"
	kind := sessionapi.KindTask
	parentID := "sess_parent"

	session, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &markdown,
		SessionKind:     &kind,
		ParentSessionID: &parentID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if session.SessionKind == nil || *session.SessionKind != sessionapi.KindTask {
		t.Fatalf("session_kind: got %#v", session.SessionKind)
	}
	if session.CurrentSpecVersion != nil {
		t.Fatalf("child should not have current_spec_version: %#v", session.CurrentSpecVersion)
	}
}

func TestCreateSessionRejectsPublicSessionKind(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := `{"spec_markdown":"---\nversion: v0\nname: bad-kind\nplatform: local\n---\n# Bad Kind\n","session_kind":"controller"}`
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, data)
	}
}

func TestCreateSessionRejectsInvalidNameWithoutStrayCompileFiles(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: ..\nplatform: local\n---\n# Task\n"

	if _, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown}); err == nil {
		t.Fatal("expected invalid spec name")
	}

	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Base(path) == "SPEC.md" {
			t.Fatalf("unexpected compile artifact outside .compile: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected failed create to clean session dir, found %d entries", len(entries))
	}
}

func TestCloudCreateSessionCreatesRootForOperatorApply(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"

	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if session.SessionKind == nil || *session.SessionKind != sessionapi.KindController {
		t.Fatalf("session_kind: got %#v", session.SessionKind)
	}
	if !strings.HasPrefix(session.SessionID, "sess_") {
		t.Fatalf("cloud session id: got %q", session.SessionID)
	}
	if session.CurrentSpecVersion == nil || *session.CurrentSpecVersion != 1 {
		t.Fatalf("current_spec_version: got %#v", session.CurrentSpecVersion)
	}
	if len(session.SpecVersions) != 1 {
		t.Fatalf("spec_versions: got %#v", session.SpecVersions)
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(root, session.SessionID, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.ApplyPackageDigest == nil || *manifest.ApplyPackageDigest == "" {
		t.Fatalf("missing apply_package_digest: %#v", manifest.ApplyPackageDigest)
	}
	if manifest.ApplyPackageLock == nil || manifest.ApplyPackageLock.RootSpecPath != "SPEC.md" {
		t.Fatalf("apply_package_lock: %#v", manifest.ApplyPackageLock)
	}
	if got := session.SpecVersions[0]["apply_package_digest"]; got != *manifest.ApplyPackageDigest {
		t.Fatalf("spec version apply_package_digest: got %#v want %q", got, *manifest.ApplyPackageDigest)
	}
}

func TestCloudCreateSessionFromApplyPackage(t *testing.T) {
	srcDir := t.TempDir()
	specPath := filepath.Join(srcDir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: v0\nname: postgres\nplatform: cloud\nskills:\n  - alpha\n---\n# Postgres\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(srcDir, "alpha")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: alpha\n---\nUse alpha."), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	pkg, err := spec.BuildApplyPackage(compiled, spec.ApplyPackageOptions{CompilerVersion: "test"})
	if err != nil {
		t.Fatalf("BuildApplyPackage: %v", err)
	}
	packagePath := filepath.Join(t.TempDir(), "package.tar.gz")
	if err := os.WriteFile(packagePath, pkg.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	session, err := store.Create(sessionapi.SessionCreateRequest{
		ApplyPackagePath:   packagePath,
		ApplyPackageDigest: pkg.Digest,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(root, session.SessionID, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.SourceSpecPath == nil || !strings.Contains(*manifest.SourceSpecPath, filepath.Join(session.SessionID, "package", "SPEC.md")) {
		t.Fatalf("source_spec_path: %#v", manifest.SourceSpecPath)
	}
	if manifest.ApplyPackageDigest == nil || *manifest.ApplyPackageDigest != pkg.Digest {
		t.Fatalf("apply_package_digest: got %#v want %q", manifest.ApplyPackageDigest, pkg.Digest)
	}
	if manifest.ApplyPackageLock == nil || manifest.ApplyPackageLock.PackageDigest != pkg.Digest {
		t.Fatalf("apply_package_lock: %#v", manifest.ApplyPackageLock)
	}
	recompiled, err := spec.CompileEnvironmentWithBase(*manifest.SessionSpecPath, filepath.Dir(*manifest.SourceSpecPath))
	if err != nil {
		t.Fatalf("CompileEnvironmentWithBase session spec: %v", err)
	}
	var alphaPath string
	for _, skill := range recompiled.Skills {
		if skill.Name == "alpha" {
			alphaPath = skill.Path
		}
	}
	if !strings.Contains(alphaPath, filepath.Join(session.SessionID, "package", "skills", "alpha")) {
		t.Fatalf("alpha resolved outside extracted package: %q", alphaPath)
	}
}

func TestUpdateSpecFromPackageDigest(t *testing.T) {
	root := t.TempDir()
	packageRoot := t.TempDir()
	t.Setenv("TELOS_PACKAGE_ROOT", packageRoot)

	first := buildTestApplyPackage(t, "postgres", "first")
	second := buildTestApplyPackage(t, "postgres", "second")
	firstPath := writePackageBlob(t, packageRoot, first)
	writePackageBlob(t, packageRoot, second)

	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	session, err := store.Create(sessionapi.SessionCreateRequest{
		ApplyPackagePath:   firstPath,
		ApplyPackageDigest: first.Digest,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	response, err := store.UpdateSpec("postgres", sessionapi.SessionSpecUpdateRequest{
		PackageDigest: second.Digest,
	})
	if err != nil {
		t.Fatalf("UpdateSpec: %v", err)
	}
	if response.Operation != "updated" || response.Session == nil || response.Session.SessionID != session.SessionID {
		t.Fatalf("response: %+v", response)
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(root, session.SessionID, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.ApplyPackageDigest == nil || *manifest.ApplyPackageDigest != second.Digest {
		t.Fatalf("apply_package_digest: got %#v want %q", manifest.ApplyPackageDigest, second.Digest)
	}
	if manifest.SourceSpecPath == nil || !strings.Contains(*manifest.SourceSpecPath, filepath.Join(session.SessionID, "package", "SPEC.md")) {
		t.Fatalf("source_spec_path: %#v", manifest.SourceSpecPath)
	}
	data, err := os.ReadFile(*manifest.SessionSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "second") {
		t.Fatalf("session spec not updated from package: %q", data)
	}
}

func buildTestApplyPackage(t *testing.T, name string, body string) *spec.ApplyPackage {
	t.Helper()
	srcDir := t.TempDir()
	specPath := filepath.Join(srcDir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: v0\nname: "+name+"\nplatform: cloud\n---\n"+body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	pkg, err := spec.BuildApplyPackage(compiled, spec.ApplyPackageOptions{CompilerVersion: "test"})
	if err != nil {
		t.Fatalf("BuildApplyPackage: %v", err)
	}
	return pkg
}

func writePackageBlob(t *testing.T, root string, pkg *spec.ApplyPackage) string {
	t.Helper()
	hex := strings.TrimPrefix(pkg.Digest, "sha256:")
	path := filepath.Join(root, "blobs", "sha256", hex, "package.tar.gz")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pkg.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCloudCreateSessionFromApplyPackageRejectsDigestMismatch(t *testing.T) {
	srcDir := t.TempDir()
	specPath := filepath.Join(srcDir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatalf("CompileEnvironment: %v", err)
	}
	pkg, err := spec.BuildApplyPackage(compiled, spec.ApplyPackageOptions{CompilerVersion: "test"})
	if err != nil {
		t.Fatalf("BuildApplyPackage: %v", err)
	}
	packagePath := filepath.Join(t.TempDir(), "package.tar.gz")
	if err := os.WriteFile(packagePath, pkg.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}

	store := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	_, err = store.Create(sessionapi.SessionCreateRequest{
		ApplyPackagePath:   packagePath,
		ApplyPackageDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	})
	if err == nil {
		t.Fatal("expected digest mismatch")
	}
	if !strings.Contains(err.Error(), "does not match expected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloudCreateSessionRejectsDuplicateLiveRoot(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"
	if _, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err == nil {
		t.Fatal("expected duplicate root to fail")
	}
	if !strings.Contains(err.Error(), "root session \"postgres\" already exists") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloudCreateSessionIgnoresFailedRootHistory(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"
	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	manifestPath := filepath.Join(root, session.SessionID, "session.json")
	m, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	finishedAt := "2026-05-19T00:00:00Z"
	result := "failed"
	m.Epochs = append(m.Epochs, sessionapi.Epoch{
		ID:         1,
		StartedAt:  "2026-05-19T00:00:00Z",
		FinishedAt: &finishedAt,
		Result:     &result,
	})
	if err := sessionapi.WriteManifest(manifestPath, m); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	recreated, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("recreate after failed history: %v", err)
	}
	if recreated.SessionID == session.SessionID {
		t.Fatal("expected a new session")
	}
}

func TestCreateSessionJSONShape(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := createSessionBody(t, "test")
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Verify the raw JSON has the expected top-level keys.
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	requiredKeys := []string{
		"session_id", "status", "runtime", "config", "provenance",
		"specs", "epochs", "spec_versions",
	}
	for _, key := range requiredKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("missing required key %q in response JSON", key)
		}
	}

	// config, provenance, specs, epochs, spec_versions must be objects/arrays.
	assertJSONType(t, m, "config", "map")
	assertJSONType(t, m, "provenance", "map")
	assertJSONType(t, m, "specs", "slice")
	assertJSONType(t, m, "epochs", "slice")
	assertJSONType(t, m, "spec_versions", "slice")
}

func TestCreateSessionRejectsUnknownFields(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(
		srv.URL+"/api/sessions",
		"application/json",
		strings.NewReader(`{"spec_markdown":"---\nversion: v0\nname: test\nplatform: local\n---\n# Test\n","unexpected":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "400", itoa(resp.StatusCode))
}

func TestCreateSessionRejectsOversizedBody(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := `{"spec_markdown":"` + strings.Repeat("x", 4<<20) + `"}`
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "400", itoa(resp.StatusCode))
}

// --------- PUT /api/sessions/{name}/spec ---------------------------------------------------------------------------------------------------------------------------------------------------------------

func TestApplySessionSpecUpdatesExistingRoot(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()
	rootSession, _ := writeAuthorizedSession(t, store.Root, "postgres", sessionapi.KindController, nil)

	updated := "---\nversion: v0\nname: postgres\nplatform: cloud\ninterval: 5m\n---\n# Postgres v2\n"
	body, err := json.Marshal(sessionapi.SessionSpecUpdateRequest{
		SpecMarkdown: updated,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/sessions/postgres/spec", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var update sessionapi.SessionSpecUpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&update); err != nil {
		t.Fatal(err)
	}
	if update.Operation != "updated" {
		t.Fatalf("operation: got %q", update.Operation)
	}
	session := update.Session
	if session == nil {
		t.Fatal("missing session")
	}
	if session.SessionID != rootSession.SessionID {
		t.Fatalf("session_id: got %q want %q", session.SessionID, rootSession.SessionID)
	}
	if session.SpecName == nil || *session.SpecName != "postgres" {
		t.Fatalf("spec_name: got %#v", session.SpecName)
	}
	if session.Specs[0].IntervalSeconds == nil || *session.Specs[0].IntervalSeconds != 300 {
		t.Fatalf("interval: got %#v", session.Specs[0].IntervalSeconds)
	}
	if session.CurrentSpecVersion == nil || *session.CurrentSpecVersion != 2 {
		t.Fatalf("current_spec_version: got %#v", session.CurrentSpecVersion)
	}
	if len(session.SpecVersions) != 1 {
		t.Fatalf("spec_versions: got %#v", session.SpecVersions)
	}
	if session.SpecVersions[0]["previous_version"].(float64) != 1 {
		t.Fatalf("previous_version: got %#v", session.SpecVersions[0])
	}
	manifest, err := sessionapi.ReadManifest(filepath.Join(store.Root, session.SessionID, "session.json"))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.ApplyPackageDigest == nil || *manifest.ApplyPackageDigest == "" {
		t.Fatalf("missing apply_package_digest: %#v", manifest.ApplyPackageDigest)
	}
	if manifest.ApplyPackageLock == nil || manifest.ApplyPackageLock.Spec.Name != "postgres" {
		t.Fatalf("apply_package_lock: %#v", manifest.ApplyPackageLock)
	}
	if got := session.SpecVersions[0]["apply_package_digest"]; got != *manifest.ApplyPackageDigest {
		t.Fatalf("spec version apply_package_digest: got %#v want %q", got, *manifest.ApplyPackageDigest)
	}
	data, err := os.ReadFile(*session.SessionSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != updated {
		t.Fatalf("spec was not updated: %q", string(data))
	}
}

func TestApplySessionSpecCreatesRootWhenMissing(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	markdown := "---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"
	body, err := json.Marshal(sessionapi.SessionSpecUpdateRequest{SpecMarkdown: markdown})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/sessions/postgres/spec", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var update sessionapi.SessionSpecUpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&update); err != nil {
		t.Fatal(err)
	}
	if update.Operation != "created" {
		t.Fatalf("operation: got %q", update.Operation)
	}
	if update.Session == nil || update.Session.SpecName == nil || *update.Session.SpecName != "postgres" {
		t.Fatalf("session: got %#v", update.Session)
	}
}

func TestApplySessionSpecRejectsNameMismatch(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	body := `{"spec_markdown":"---\nversion: v0\nname: redis\nplatform: cloud\n---\n# Redis\n"}`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/sessions/postgres/spec", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, data)
	}
}

func TestGetRootSessionSpec(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()
	rootSession, _ := writeAuthorizedSession(t, store.Root, "postgres", sessionapi.KindController, nil)

	resp, err := http.Get(srv.URL + "/api/sessions/" + rootSession.SessionID + "/spec")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["dir_name"]; !ok {
		t.Fatalf("expected dir_name in response: %#v", raw)
	}
	if _, ok := raw["dirName"]; ok {
		t.Fatalf("unexpected camelCase dirName in response: %#v", raw)
	}
	if _, ok := raw["spec_path"]; ok {
		t.Fatalf("unexpected spec_path in response: %#v", raw)
	}
	if _, ok := raw["specPath"]; ok {
		t.Fatalf("unexpected camelCase specPath in response: %#v", raw)
	}
	var body sessionapi.SessionSpecResponse
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body.DirName != "postgres" {
		t.Fatalf("dir_name: got %q", body.DirName)
	}
	if !strings.Contains(body.Markdown, "# postgres") {
		t.Fatalf("markdown: got %q", body.Markdown)
	}
	if body.Environment != `{"name":"postgres","platform":"cloud","version":"v0"}` {
		t.Fatalf("environment: got %q", body.Environment)
	}
}

func TestGetRootSessionSpecUsesLineageNotKind(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()
	markdown := "---\nversion: v0\nname: postgres\nplatform: local\n---\n# Postgres\n"
	kind := sessionapi.KindTask
	root, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		SessionKind:  &kind,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/sessions/" + root.SessionID + "/spec")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var body sessionapi.SessionSpecResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Version == nil || *body.Version != 1 {
		t.Fatalf("version: got %#v", body.Version)
	}
}

func TestApplySessionSpecRejectsDuplicateActiveRoots(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()
	first, _ := writeAuthorizedSession(t, store.Root, "sess_first", sessionapi.KindController, nil)
	second, _ := writeAuthorizedSession(t, store.Root, "sess_second", sessionapi.KindController, nil)
	for _, session := range []sessionapi.Manifest{first, second} {
		manifestPath := filepath.Join(store.Root, session.SessionID, "session.json")
		m, err := sessionapi.ReadManifest(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		m.ParentSessionID = nil
		m.SpecName = "postgres"
		if len(m.Specs) > 0 {
			m.Specs[0].Name = "postgres"
			m.Specs[0].DirName = "postgres"
		}
		if err := sessionapi.WriteManifest(manifestPath, m); err != nil {
			t.Fatal(err)
		}
	}

	body := `{"spec_markdown":"---\nversion: v0\nname: postgres\nplatform: cloud\n---\n# Postgres\n"}`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/sessions/postgres/spec", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 409, got %d: %s", resp.StatusCode, data)
	}
}

// --------- GET /api/sessions ------------------------------------------------------------------------------------------------------------------------------------------------------------------

func TestListSessions(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	// Initially empty.
	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status", "200", itoa(resp.StatusCode))

	var listResp sessionapi.SessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(listResp.Sessions))
	}

	// Create two sessions, list should return them newest first.
	post(t, srv.URL+"/api/sessions", createSessionBody(t, "a"))
	post(t, srv.URL+"/api/sessions", createSessionBody(t, "b"))

	resp2, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	var listResp2 sessionapi.SessionListResponse
	json.NewDecoder(resp2.Body).Decode(&listResp2)
	if len(listResp2.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(listResp2.Sessions))
	}

	// Both must have the full Session shape.
	for _, s := range listResp2.Sessions {
		assertNonEmpty(t, "session_id", s.SessionID)
		assertEqual(t, "runtime", string(s.Runtime), "local")
	}
}

func TestListSessionsLimit(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	post(t, srv.URL+"/api/sessions", createSessionBody(t, "a"))
	time.Sleep(2 * time.Millisecond)
	post(t, srv.URL+"/api/sessions", createSessionBody(t, "b"))
	time.Sleep(2 * time.Millisecond)
	post(t, srv.URL+"/api/sessions", createSessionBody(t, "c"))

	resp, err := http.Get(srv.URL + "/api/sessions?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status", "200", itoa(resp.StatusCode))

	var listResp sessionapi.SessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Sessions) != 2 {
		t.Fatalf("limited sessions: got %d, want 2", len(listResp.Sessions))
	}
	if listResp.Sessions[0].SpecName == nil || *listResp.Sessions[0].SpecName != "c" ||
		listResp.Sessions[1].SpecName == nil || *listResp.Sessions[1].SpecName != "b" {
		t.Fatalf("limited sessions: got %#v, want newest c,b", listResp.Sessions)
	}
}

func TestListSessionsHidesChildrenByDefault(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	root := createSession(t, srv.URL, createSessionBody(t, "root"))
	childSpec := "---\nversion: v0\nname: child\nplatform: local\n---\n# Child\n"
	if _, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &childSpec,
		ParentSessionID: &root.SessionID,
	}); err != nil {
		t.Fatalf("create child: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var listResp sessionapi.SessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Sessions) != 1 || listResp.Sessions[0].SessionID != root.SessionID {
		t.Fatalf("default list should return only roots, got %#v", listResp.Sessions)
	}
}

func TestListSessionsCanIncludeChildren(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	root := createSession(t, srv.URL, createSessionBody(t, "root"))
	childSpec := "---\nversion: v0\nname: child\nplatform: local\n---\n# Child\n"
	child, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &childSpec,
		ParentSessionID: &root.SessionID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/sessions?include_children=true")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var listResp sessionapi.SessionListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Sessions) != 2 {
		t.Fatalf("include_children list count: got %d", len(listResp.Sessions))
	}
	ids := map[string]bool{}
	for _, session := range listResp.Sessions {
		ids[session.SessionID] = true
	}
	if !ids[root.SessionID] || !ids[child.SessionID] {
		t.Fatalf("include_children sessions: got %#v", listResp.Sessions)
	}
}

func TestListSessionsRejectsInvalidIncludeChildren(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/sessions?include_children=sure")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "400", itoa(resp.StatusCode))
}

func TestListSessionsRejectsInvalidLimit(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/sessions?limit=-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status", "400", itoa(resp.StatusCode))
}

func TestListSessionsJSONShape(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	createSession(t, srv.URL, createSessionBody(t, "summary"))

	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	assertJSONType(t, m, "sessions", "slice")
	sessions := m["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions: got %#v", sessions)
	}
	session := sessions[0].(map[string]any)
	for _, key := range []string{
		"session_kind",
		"session_spec_path",
		"session_dir",
		"active_workspace_path",
		"config",
		"provenance",
		"specs",
		"epochs",
		"spec_versions",
	} {
		if _, ok := session[key]; ok {
			t.Fatalf("list summary should not include %q: %#v", key, session)
		}
	}
	for _, key := range []string{"session_id", "spec_name", "status", "runtime"} {
		if _, ok := session[key]; !ok {
			t.Fatalf("list summary missing %q: %#v", key, session)
		}
	}
}

// --------- GET /api/sessions/{id} ---------------------------------------------------------------------------------------------------------------------------------------------------

func TestGetSession(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBodyWithConfig(t, "x", "m", "high"))

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var session sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&session)
	assertEqual(t, "session_id", created.SessionID, session.SessionID)
	assertEqual(t, "status", "pending", string(session.Status))
}

func TestGetSessionNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/sessions/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "404", itoa(resp.StatusCode))
}

// --------- POST /api/sessions/{id}/stop ---------------------------------------------------------------------------------------------------------------------------------

func TestStopSession(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "s"))

	req, _ := http.NewRequest("POST", srv.URL+"/api/sessions/"+created.SessionID+"/stop", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var session sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&session)
	assertEqual(t, "status", "stopped", string(session.Status))

	if session.Error == nil || *session.Error != "stopped by operator" {
		t.Errorf("expected error='stopped by operator', got %v", session.Error)
	}
}

func TestStopAlreadyStopped(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "s2"))

	// Stop twice - second should be idempotent.
	stopSession(t, srv.URL, created.SessionID)
	session := stopSession(t, srv.URL, created.SessionID)
	assertEqual(t, "status", "stopped", string(session.Status))
}

func TestStopSessionNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/api/sessions/nonexistent/stop", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "404", itoa(resp.StatusCode))
}

// --------- GET /api/sessions/{id}/transcript ------------------------------------------------------------------------------------------------------------------

func TestTranscriptNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "t"))

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "404", itoa(resp.StatusCode))
}

func TestTranscriptPresent(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "tp"))

	// Write a transcript file in the expected location.
	transcriptPath := filepath.Join(store.Root, created.SessionID, "specs", "tp",
		"transcript-"+created.SessionID+".md")
	os.MkdirAll(filepath.Dir(transcriptPath), 0o755)
	os.WriteFile(transcriptPath, []byte("# Transcript\nHello"), 0o644)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))
	assertEqual(t, "content-type", "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "# Transcript\nHello" {
		t.Errorf("unexpected transcript body: %q", body)
	}
}

// --------- GET /api/sessions/{id}/events ------------------------------------------------------------------------------------------------------------------------------

func TestEventsEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "e"))

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var evResp sessionapi.SessionEventsResponse
	json.NewDecoder(resp.Body).Decode(&evResp)
	if len(evResp.Events) != 0 {
		t.Errorf("expected 0 events, got %d", len(evResp.Events))
	}
}

func TestEventsPresent(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "ep"))

	// Write evidence JSONL.
	evidencePath := filepath.Join(store.Root, created.SessionID, "specs", "ep", "evidence.jsonl")
	os.MkdirAll(filepath.Dir(evidencePath), 0o755)
	lines := []string{
		`{"event":"agent_complete","data":{"cost_usd":0.5,"role":"prover"}}`,
		`{"event":"game_end","data":{"game_result":"accepted"}}`,
	}
	os.WriteFile(evidencePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var evResp sessionapi.SessionEventsResponse
	json.NewDecoder(resp.Body).Decode(&evResp)

	if len(evResp.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evResp.Events))
	}

	// Verify event shape matches the Python contract.
	assertEqual(t, "event[0].event", "agent_complete", evResp.Events[0].Event)
	if evResp.Events[0].SpecName == nil || *evResp.Events[0].SpecName != "ep" {
		t.Errorf("expected spec_name=ep, got %v", evResp.Events[0].SpecName)
	}
	if evResp.Events[0].Data == nil {
		t.Fatal("expected non-nil data")
	}
	if evResp.Events[0].Data["cost_usd"] != 0.5 {
		t.Errorf("expected cost_usd=0.5, got %v", evResp.Events[0].Data["cost_usd"])
	}

	assertEqual(t, "event[1].event", "game_end", evResp.Events[1].Event)
}

func TestEventsJSONShape(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "ejs"))

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	assertJSONType(t, m, "events", "slice")
}

// --------- GET /api/sessions/{id}/diagnostics ------------------------------------------------------------------------------------------------------------------------------

func TestDiagnosticsAggregatesBudgetsRetriesStopsAndArtifacts(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	markdown := "---\nversion: v0\nname: diag\nplatform: local\n---\n# Diagnostics\n"
	maxRounds := 4
	maxDuration := 3600
	maxInputTokens := 1200
	maxOutputTokens := 400
	maxToolLoops := 55
	agentTimeout := 120
	created, err := store.Create(sessionapi.SessionCreateRequest{
		SpecMarkdown:    &markdown,
		MaxRounds:       &maxRounds,
		MaxDurationSec:  &maxDuration,
		MaxInputTokens:  &maxInputTokens,
		MaxOutputTokens: &maxOutputTokens,
		MaxToolLoops:    &maxToolLoops,
		AgentTimeoutSec: &agentTimeout,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	evidence := strings.Join([]string{
		`{"event":"budget_exceeded","round":1,"role":"prover","data":{"budget":"max_input_tokens"}}`,
		`{"event":"cost_cap_unenforceable","round":1,"role":"prover","data":{"max_cost_usd":10,"reason":"provider returned no cost"}}`,
		`{"event":"agent_failure_recoverable","round":1,"role":"prover","data":{"error":"provider_rate_limited: HTTP 429"}}`,
		`{"event":"agent_failure_recoverable","round":1,"role":"verifier","data":{"error_code":"agent_protocol","error":"missing status"}}`,
		`{"event":"game_error","round":1,"role":"system","data":{"error":"benchmark verifier rejected final artifact"}}`,
		`{"event":"game_end","round":2,"data":{"game_result":"failure","completion_reason":"runtime_budget_exhausted","total_cost_usd":0.42,"cost_unavailable":true,"total_input_tokens":1300,"total_output_tokens":210,"prover_rounds":1,"verifier_rounds":1}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(*created.Specs[0].EvidencePath, []byte(evidence), 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}

	ledger := []byte(`{"session_id":"` + created.SessionID + `","turns":[]}` + "\n")
	if err := os.WriteFile(*created.Specs[0].ObjectiveLedgerPath, ledger, 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	turnDir := filepath.Join(store.Root, created.SessionID, "specs", "diag", "turns", "0001-prover")
	if err := os.MkdirAll(turnDir, 0o755); err != nil {
		t.Fatalf("create turn dir: %v", err)
	}
	sessionLog := strings.Join([]string{
		`{"type":"model_request","data":{"sequence":1}}`,
		`{"type":"retry","data":{"sequence":1,"attempt":2,"delay_ms":250,"error_code":"provider_rate_limited","error":"rate limited","provider_status_code":429}}`,
		`{"type":"model_response","data":{"sequence":1,"response_id":"resp_1","stop_reason":"tool_calls"}}`,
		`{"type":"tool_result","data":{"tool_call_id":"call_1","tool_name":"read_file","duration_ms":5,"output_bytes":12}}`,
		`{"type":"tool_result","data":{"tool_call_id":"call_2","tool_name":"bash","is_error":true,"error_code":"tool_timeout","duration_ms":1000,"output_bytes":64}}`,
		`{"type":"reasoning_sanitized","data":{"words_removed":4}}`,
		`{"type":"outside_workspace_access","data":{"action":"write_file","path":"/tmp/telos-scratch/out.txt","write":true}}`,
		`{"type":"error","data":{"sequence":2,"error_code":"agent_incomplete","error":"agent_incomplete: no final response","retryable":false}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(turnDir, "session.jsonl"), []byte(sessionLog), 0o644); err != nil {
		t.Fatalf("write session log: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var diagnostics sessionapi.SessionDiagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&diagnostics); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	assertEqual(t, "session_id", created.SessionID, diagnostics.SessionID)
	if diagnostics.Limits.MaxRounds != 4 ||
		diagnostics.Limits.MaxDurationSec != 3600 ||
		diagnostics.Limits.MaxInputTokens != 1200 ||
		diagnostics.Limits.MaxOutputTokens != 400 ||
		diagnostics.Limits.MaxToolLoops != 55 ||
		diagnostics.Limits.AgentTimeoutSec != 120 {
		t.Fatalf("limits not surfaced: %#v", diagnostics.Limits)
	}
	if diagnostics.BudgetExceeded["max_input_tokens"] != 1 || diagnostics.BudgetExceeded["cost_cap_unenforceable"] != 1 {
		t.Fatalf("budget exceeded counts: %#v", diagnostics.BudgetExceeded)
	}
	if !diagnostics.Totals.CostUnavailable || len(diagnostics.Specs) != 1 || !diagnostics.Specs[0].Totals.CostUnavailable {
		t.Fatalf("cost availability not surfaced: totals=%#v specs=%#v", diagnostics.Totals, diagnostics.Specs)
	}
	if diagnostics.Failures["provider"] != 1 ||
		diagnostics.Failures["protocol"] != 1 ||
		diagnostics.Failures["task_budget"] != 1 ||
		diagnostics.Failures["benchmark_verifier_failure"] != 1 ||
		diagnostics.Failures["tool"] != 1 ||
		diagnostics.Failures["agent_incomplete"] != 1 {
		t.Fatalf("failure taxonomy: %#v", diagnostics.Failures)
	}
	if diagnostics.Specs[0].Failures["tool"] != 1 || diagnostics.Specs[0].Failures["agent_incomplete"] != 1 {
		t.Fatalf("spec failure taxonomy should include session-log failures: %#v", diagnostics.Specs[0].Failures)
	}
	if diagnostics.StopReasons["tool_calls"] != 1 {
		t.Fatalf("stop reasons: %#v", diagnostics.StopReasons)
	}
	if diagnostics.SessionLogEvents["tool_result"] != 2 || diagnostics.SessionLogEvents["reasoning_sanitized"] != 1 {
		t.Fatalf("session log event counts: %#v", diagnostics.SessionLogEvents)
	}
	if len(diagnostics.OutsideWorkspace) != 1 || diagnostics.OutsideWorkspace[0].Path != "/tmp/telos-scratch/out.txt" || !diagnostics.OutsideWorkspace[0].Write {
		t.Fatalf("outside workspace diagnostics: %#v", diagnostics.OutsideWorkspace)
	}
	if len(diagnostics.Retries) != 1 || diagnostics.Retries[0].ErrorCode != "provider_rate_limited" || diagnostics.Retries[0].ProviderStatusCode != 429 {
		t.Fatalf("retry diagnostics: %#v", diagnostics.Retries)
	}
	if len(diagnostics.Errors) != 2 || !diagnosticsHasErrorCode(diagnostics.Errors, "tool_timeout") || !diagnosticsHasErrorCode(diagnostics.Errors, "agent_incomplete") {
		t.Fatalf("error diagnostics: %#v", diagnostics.Errors)
	}
	if len(diagnostics.Artifacts) != 1 || !diagnostics.Artifacts[0].EvidenceExists || !diagnostics.Artifacts[0].ObjectiveLedgerExists {
		t.Fatalf("artifact diagnostics: %#v", diagnostics.Artifacts)
	}
	if diagnostics.Totals.InputTokens != 1300 || diagnostics.Totals.OutputTokens != 210 || diagnostics.Totals.Rounds != 2 {
		t.Fatalf("totals: %#v", diagnostics.Totals)
	}
}

func TestDiagnosticsDedupsFailuresBetweenEvidenceAndSessionLog(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	markdown := createSessionBody(t, "dedup")
	created := createSession(t, srv.URL, markdown)

	// The same recoverable turn failure (provider_rate_limited) is recorded in
	// evidence as agent_failure_recoverable AND in the turn's session.jsonl as
	// an error event with the same error_code. It must be counted once.
	evidence := strings.Join([]string{
		`{"event":"agent_failure_recoverable","round":1,"role":"prover","data":{"error_code":"provider_rate_limited","error":"provider_rate_limited: HTTP 429"}}`,
		`{"event":"game_end","round":2,"data":{"game_result":"failure","completion_reason":"runtime_budget_exhausted","total_cost_usd":0.1,"total_input_tokens":100,"total_output_tokens":20,"prover_rounds":1,"verifier_rounds":1}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(*created.Specs[0].EvidencePath, []byte(evidence), 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}

	turnDir := filepath.Join(store.Root, created.SessionID, "specs", "dedup", "turns", "0001-prover")
	if err := os.MkdirAll(turnDir, 0o755); err != nil {
		t.Fatalf("create turn dir: %v", err)
	}
	// The session-log error event carries the SAME error_code as the evidence
	// agent_failure_recoverable above — this is the double-count scenario.
	sessionLog := strings.Join([]string{
		`{"type":"error","data":{"sequence":1,"error_code":"provider_rate_limited","error":"provider_rate_limited: HTTP 429","retryable":true}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(turnDir, "session.jsonl"), []byte(sessionLog), 0o644); err != nil {
		t.Fatalf("write session log: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var diagnostics sessionapi.SessionDiagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&diagnostics); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	// The provider failure must be counted exactly once despite appearing in
	// both evidence and the session log.
	if diagnostics.Failures["provider"] != 1 {
		t.Fatalf("provider failure should be deduped to 1, got %d: %#v", diagnostics.Failures["provider"], diagnostics.Failures)
	}
	// The per-turn Errors list still records the granular session-log error,
	// even though its failure was deduped from the tally.
	if len(diagnostics.Errors) != 1 || diagnostics.Errors[0].ErrorCode != "provider_rate_limited" {
		t.Fatalf("session-log error should still be in Errors list: %#v", diagnostics.Errors)
	}
}

func TestDiagnosticsDedupsFailuresWhenSpecNameDiffersFromDirName(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	markdown := createSessionBody(t, "dedup")
	created := createSession(t, srv.URL, markdown)

	manifestPath := filepath.Join(store.Root, created.SessionID, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifest.Specs[0].Name = "canonical-dedup"
	manifest.Specs[0].DirName = "dedup"
	if err := sessionapi.WriteManifest(manifestPath, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	evidence := strings.Join([]string{
		`{"event":"agent_failure_recoverable","spec_name":"canonical-dedup","round":1,"role":"prover","data":{"error_code":"provider_rate_limited","error":"provider_rate_limited: HTTP 429"}}`,
		`{"event":"game_end","spec_name":"canonical-dedup","round":2,"data":{"game_result":"failure","completion_reason":"runtime_budget_exhausted","total_cost_usd":0.1,"total_input_tokens":100,"total_output_tokens":20,"prover_rounds":1,"verifier_rounds":1}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(*created.Specs[0].EvidencePath, []byte(evidence), 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}

	turnDir := filepath.Join(store.Root, created.SessionID, "specs", "dedup", "turns", "0001-prover")
	if err := os.MkdirAll(turnDir, 0o755); err != nil {
		t.Fatalf("create turn dir: %v", err)
	}
	sessionLog := `{"type":"error","data":{"sequence":1,"error_code":"provider_rate_limited","error":"provider_rate_limited: HTTP 429","retryable":true}}` + "\n"
	if err := os.WriteFile(filepath.Join(turnDir, "session.jsonl"), []byte(sessionLog), 0o644); err != nil {
		t.Fatalf("write session log: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	var diagnostics sessionapi.SessionDiagnosticsResponse
	if err := json.NewDecoder(resp.Body).Decode(&diagnostics); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	if diagnostics.Failures["provider"] != 1 {
		t.Fatalf("provider failure should be deduped across spec aliases, got %d: %#v", diagnostics.Failures["provider"], diagnostics.Failures)
	}
	if len(diagnostics.Specs) != 1 || diagnostics.Specs[0].Name != "canonical-dedup" || diagnostics.Specs[0].DirName != "dedup" {
		t.Fatalf("spec diagnostics: %#v", diagnostics.Specs)
	}
}

func TestEventsSSE(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "es"))
	evidencePath := filepath.Join(store.Root, created.SessionID, "specs", "es", "evidence.jsonl")
	os.MkdirAll(filepath.Dir(evidencePath), 0o755)
	os.WriteFile(evidencePath, []byte(`{"event":"game_end","data":{"game_result":"success"}}`+"\n"), 0o644)
	stopSession(t, srv.URL, created.SessionID)

	req, err := http.NewRequest("GET", srv.URL+"/api/sessions/"+created.SessionID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type: got %q", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `data: {"event":"game_end"`) {
		t.Fatalf("unexpected SSE body: %s", body)
	}
}

func TestGetSessionHydratesEvidenceSummary(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: evidence-summary\nplatform: local\n---\n# Evidence\n"

	created, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.Specs) != 1 || created.Specs[0].EvidencePath == nil {
		t.Fatalf("missing evidence path: %#v", created.Specs)
	}
	evidence := `{"event":"agent_complete","round":1,"data":{"cost_usd":0.10}}` + "\n" +
		`{"event":"game_end","round":2,"data":{"total_cost_usd":1.23,"cost_unavailable":true,"total_input_tokens":100,"total_output_tokens":30,"total_cache_read_tokens":7,"total_cache_creation_tokens":5,"prover_rounds":1,"verifier_rounds":1,"completion_reason":"review_cycles_complete","verifier_conceded":false}}` + "\n"
	if err := os.WriteFile(*created.Specs[0].EvidencePath, []byte(evidence), 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}

	session, err := store.Get(created.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if session.TotalCostUSD == nil || *session.TotalCostUSD != 1.23 {
		t.Fatalf("cost: got %v", session.TotalCostUSD)
	}
	if session.CostUnavailable == nil || !*session.CostUnavailable {
		t.Fatalf("cost unavailable: got %v", session.CostUnavailable)
	}
	if len(session.Specs) != 1 || session.Specs[0].CostUnavailable == nil || !*session.Specs[0].CostUnavailable {
		t.Fatalf("spec cost unavailable: %#v", session.Specs)
	}
	if session.TotalInputTokens == nil || *session.TotalInputTokens != 100 {
		t.Fatalf("input tokens: got %v", session.TotalInputTokens)
	}
	if session.TotalOutputTokens == nil || *session.TotalOutputTokens != 30 {
		t.Fatalf("output tokens: got %v", session.TotalOutputTokens)
	}
	if session.TotalCacheReadTokens == nil || *session.TotalCacheReadTokens != 7 {
		t.Fatalf("cache read tokens: got %v", session.TotalCacheReadTokens)
	}
	if session.TotalCacheCreateTokens == nil || *session.TotalCacheCreateTokens != 5 {
		t.Fatalf("cache create tokens: got %v", session.TotalCacheCreateTokens)
	}
	if session.RoundCount == nil || *session.RoundCount != 2 {
		t.Fatalf("round count: got %v", session.RoundCount)
	}
	if session.CompletionReason == nil || *session.CompletionReason != "review_cycles_complete" {
		t.Fatalf("completion reason: got %v", session.CompletionReason)
	}
	if session.VerifierConceded == nil || *session.VerifierConceded {
		t.Fatalf("verifier conceded: got %v", session.VerifierConceded)
	}
}

func TestGetSessionHydratesEpochErrorCode(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: epoch-error\nplatform: local\n---\n# Error\n"

	created, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	manifestPath := filepath.Join(root, created.SessionID, "session.json")
	manifest, err := sessionapi.ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	finishedAt := "2026-06-19T10:05:00.000Z"
	result := "failed"
	errText := "runtime_budget_exhausted:max_rounds"
	errCode := "runtime_budget_exhausted"
	manifest.Epochs = []sessionapi.Epoch{{
		ID:         1,
		StartedAt:  "2026-06-19T10:00:00.000Z",
		FinishedAt: &finishedAt,
		Result:     &result,
		Error:      &errText,
		ErrorCode:  &errCode,
	}}
	if err := sessionapi.WriteManifest(manifestPath, manifest); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	session, err := store.Get(created.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if session.ErrorCode == nil || *session.ErrorCode != errCode {
		t.Fatalf("session error code: got %#v", session.ErrorCode)
	}
	if len(session.Epochs) != 1 || session.Epochs[0]["error_code"] != errCode {
		t.Fatalf("epoch error code missing: %#v", session.Epochs)
	}
}

func TestGetSessionHydratesActiveTurnFromEvidence(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	markdown := "---\nversion: v0\nname: active-turn\nplatform: local\n---\n# Active\n"

	created, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	evidence := `{"event":"game_start","round":0,"role":"system","data":{}}` + "\n" +
		`{"event":"round_start","round":1,"role":"prover","data":{}}` + "\n"
	if err := os.WriteFile(*created.Specs[0].EvidencePath, []byte(evidence), 0o644); err != nil {
		t.Fatalf("write evidence: %v", err)
	}

	session, err := store.Get(created.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if session.CurrentRound == nil || *session.CurrentRound != 1 {
		t.Fatalf("current round: got %v", session.CurrentRound)
	}
	if session.CurrentRole == nil || *session.CurrentRole != "prover" {
		t.Fatalf("current role: got %v", session.CurrentRole)
	}
	if session.CurrentSpec == nil || session.CurrentSpec.Name == nil || *session.CurrentSpec.Name != "active-turn" {
		t.Fatalf("current spec: got %#v", session.CurrentSpec)
	}

	evidence += `{"event":"agent_complete","round":1,"role":"prover","data":{"status":"CONTINUE"}}` + "\n"
	if err := os.WriteFile(*created.Specs[0].EvidencePath, []byte(evidence), 0o644); err != nil {
		t.Fatalf("write completed evidence: %v", err)
	}
	session, err = store.Get(created.SessionID)
	if err != nil {
		t.Fatalf("Get completed turn: %v", err)
	}
	if session.CurrentRound != nil || session.CurrentRole != nil {
		t.Fatalf("current turn should clear after agent_complete: round=%v role=%v", session.CurrentRound, session.CurrentRole)
	}
}

// --------- GET /api/sessions/{id}/workspace/{spec} ------------------------------------------------------------------------------------------------

func TestWorkspaceNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "w"))

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/workspace/w")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	assertEqual(t, "status_code", "404", itoa(resp.StatusCode))
}

func TestWorkspacePresent(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "wp"))

	// Create the workspace archive.
	workspacePath := filepath.Join(store.Root, created.SessionID, "specs", "wp", "workspace.tar.gz")
	os.MkdirAll(filepath.Dir(workspacePath), 0o755)
	os.WriteFile(workspacePath, []byte("fake-archive-content"), 0o644)

	// Update the manifest to include workspace_path.
	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	specs := m["specs"].([]any)
	spec0 := specs[0].(map[string]any)
	spec0["workspace_path"] = workspacePath
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	resp, err := http.Get(srv.URL + "/api/sessions/" + created.SessionID + "/workspace/wp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	assertEqual(t, "status_code", "200", itoa(resp.StatusCode))

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "fake-archive-content" {
		t.Errorf("unexpected workspace body: %q", body)
	}
}

// --------- Session lifecycle ------------------------------------------------------------------------------------------------------------------------------------------------------------------

func TestSessionLifecycleStatus(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "lc"))
	assertEqual(t, "initial status", "pending", string(created.Status))

	// Simulate an open epoch (running).
	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["epochs"] = []any{
		map[string]any{
			"id":          1,
			"started_at":  "2026-01-01T00:00:00.000Z",
			"finished_at": nil,
			"result":      nil,
			"error":       nil,
			"runner": map[string]any{
				"kind": "local-subprocess",
				"pid":  os.Getpid(),
			},
		},
	}
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	session := getSession(t, srv.URL, created.SessionID)
	assertEqual(t, "running status", "running", string(session.Status))

	// Stop it.
	stopped := stopSession(t, srv.URL, created.SessionID)
	assertEqual(t, "stopped status", "stopped", string(stopped.Status))
}

func TestSessionLifecycleStaleWhenRunnerMissing(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "stale"))

	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["epochs"] = []any{
		map[string]any{
			"id":          1,
			"started_at":  "2026-01-01T00:00:00.000Z",
			"finished_at": nil,
			"result":      nil,
			"error":       nil,
			"runner": map[string]any{
				"kind": "local-subprocess",
				"pid":  2_000_000_000,
			},
		},
	}
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	session := getSession(t, srv.URL, created.SessionID)
	assertEqual(t, "stale status", "stale", string(session.Status))
}

func TestSessionStatusCompleted(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "comp"))

	// Simulate completed epoch.
	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	finished := "2026-01-01T00:01:00.000Z"
	result := "completed"
	m["epochs"] = []any{
		map[string]any{
			"id":          1,
			"started_at":  "2026-01-01T00:00:00.000Z",
			"finished_at": finished,
			"result":      result,
			"error":       nil,
			"runner":      nil,
		},
	}
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	session := getSession(t, srv.URL, created.SessionID)
	assertEqual(t, "completed status", "completed", string(session.Status))
}

func TestCloudRootStatusStaysRunningAfterCompletedCycle(t *testing.T) {
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	markdown := "---\nversion: v0\nname: root\nplatform: cloud\n---\n# Root\n"

	created, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	mpath := filepath.Join(root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	finished := "2026-01-01T00:01:00.000Z"
	result := "completed"
	m["epochs"] = []any{
		map[string]any{
			"id":          1,
			"started_at":  "2026-01-01T00:00:00.000Z",
			"finished_at": finished,
			"result":      result,
			"error":       nil,
			"runner":      nil,
		},
	}
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	session, err := store.Get(created.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertEqual(t, "root status", "running", string(session.Status))
}

func TestSessionStatusFailed(t *testing.T) {
	srv, store := newTestServer(t)
	defer srv.Close()

	created := createSession(t, srv.URL, createSessionBody(t, "fail"))

	mpath := filepath.Join(store.Root, created.SessionID, "session.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	finished := "2026-01-01T00:01:00.000Z"
	result := "failed"
	errMsg := "some error"
	m["epochs"] = []any{
		map[string]any{
			"id":          1,
			"started_at":  "2026-01-01T00:00:00.000Z",
			"finished_at": finished,
			"result":      result,
			"error":       errMsg,
			"runner":      nil,
		},
	}
	updated, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(mpath, updated, 0o644)

	session := getSession(t, srv.URL, created.SessionID)
	assertEqual(t, "failed status", "failed", string(session.Status))
	if session.Error == nil || *session.Error != "some error" {
		t.Errorf("expected error='some error', got %v", session.Error)
	}
}

// --------- Python API JSON compatibility ------------------------------------------------------------------------------------------------------------------------------

func TestPythonManifestCompat(t *testing.T) {
	// Verify that a manifest written in the Python format can be read back
	// and produces the expected Session shape.
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)

	id := "local_20260510_131841_00"
	dir := filepath.Join(root, id)
	os.MkdirAll(dir, 0o755)

	specDir := filepath.Join(dir, "specs", "my-spec")
	os.MkdirAll(specDir, 0o755)

	evidencePath := filepath.Join(specDir, "evidence.jsonl")
	transcriptPath := filepath.Join(specDir, "transcript-"+id+".md")

	os.WriteFile(transcriptPath, []byte("# Test transcript"), 0o644)
	os.WriteFile(evidencePath, []byte(`{"event":"agent_complete","data":{"cost_usd":1.23}}`+"\n"), 0o644)

	// Write a manifest in the Python format.
	manifest := map[string]any{
		"session_id":        id,
		"session_kind":      "task",
		"created_at":        "2026-05-10T20:18:41.680Z",
		"launcher":          "local",
		"parent_session_id": nil,
		"source_spec_path":  "/tmp/my-spec/SPEC.md",
		"session_spec_path": filepath.Join(specDir, "spec.md"),
		"spec_name":         "my-spec",
		"config": map[string]any{
			"model":      "claude-opus-4-6",
			"max_rounds": 8,
			"thinking":   "medium",
		},
		"provenance": map[string]any{"mode": "local"},
		"specs": []any{
			map[string]any{
				"index":            0,
				"name":             "my-spec",
				"dir_name":         "my-spec",
				"evidence_path":    evidencePath,
				"transcript_path":  transcriptPath,
				"workspace_path":   nil,
				"interval_seconds": nil,
			},
		},
		"epochs": []any{
			map[string]any{
				"id":          1,
				"started_at":  "2026-05-10T20:18:41.682Z",
				"finished_at": "2026-05-10T20:24:55.834Z",
				"result":      "completed",
				"error":       nil,
				"runner": map[string]any{
					"kind": "local-subprocess",
					"pid":  87080,
				},
			},
		},
	}

	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(dir, "session.json"), data, 0o644)

	// Read it back via the store.
	session, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	assertEqual(t, "session_id", id, session.SessionID)
	assertEqual(t, "status", "completed", string(session.Status))
	assertEqual(t, "runtime", "local", string(session.Runtime))

	if session.SessionKind == nil || *session.SessionKind != sessionapi.KindTask {
		t.Errorf("expected session_kind=task, got %v", session.SessionKind)
	}
	if session.SpecName == nil || *session.SpecName != "my-spec" {
		t.Errorf("expected spec_name=my-spec, got %v", session.SpecName)
	}

	// Config roundtrip.
	assertConfigStr(t, session.Config, "model", "claude-opus-4-6")

	// Spec shape.
	if len(session.Specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(session.Specs))
	}
	spec := session.Specs[0]
	if spec.EvidenceExists == nil || !*spec.EvidenceExists {
		t.Error("expected evidence_exists=true")
	}
	if spec.TranscriptExists == nil || !*spec.TranscriptExists {
		t.Error("expected transcript_exists=true")
	}

	// Transcript.
	text, err := store.Transcript(id)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "transcript", "# Test transcript", text)

	// Events.
	events, err := store.Events(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	assertEqual(t, "event", "agent_complete", events[0].Event)
}

func TestBearerAuthorizerRequiresOperatorTokenForApply(t *testing.T) {
	srv, _ := newBearerTestServer(t, "operator-token")
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(createSessionBody(t, "secure")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	req, err := http.NewRequest("POST", srv.URL+"/api/sessions", strings.NewReader(createSessionBody(t, "secure")))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer operator-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201 with operator token, got %d: %s", resp.StatusCode, body)
	}
}

func TestBearerAuthorizerHonorsSessionScopedTokens(t *testing.T) {
	srv, store := newBearerTestServer(t, "operator-token")
	defer srv.Close()

	parent, parentToken := writeAuthorizedSession(t, store.Root, "sess_parent", sessionapi.KindController, nil)
	child, _ := writeAuthorizedSession(t, store.Root, "sess_child", sessionapi.KindTask, &parent.SessionID)
	other, _ := writeAuthorizedSession(t, store.Root, "sess_other", sessionapi.KindTask, nil)

	got := getSessionWithToken(t, srv.URL, child.SessionID, parentToken)
	if got.SessionID != child.SessionID {
		t.Fatalf("root token should read child session, got %q", got.SessionID)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/sessions/"+parent.SessionID+"/spec", nil)
	req.Header.Set("Authorization", "Bearer "+parentToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("root token should read its own spec, got %d: %s", resp.StatusCode, body)
	}

	req, _ = http.NewRequest("GET", srv.URL+"/api/sessions/"+other.SessionID, nil)
	req.Header.Set("Authorization", "Bearer "+parentToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("root token should not read unrelated session, got %d", resp.StatusCode)
	}

	taskToken := child.Access.APIToken
	got = getSessionWithToken(t, srv.URL, child.SessionID, taskToken)
	if got.SessionID != child.SessionID {
		t.Fatalf("child token should read itself, got %q", got.SessionID)
	}

	req, _ = http.NewRequest("POST", srv.URL+"/api/sessions", strings.NewReader(createSessionBody(t, "blocked")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+taskToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("child token should not apply, got %d", resp.StatusCode)
	}
}

// --------- Test helpers ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

func newBearerTestServer(t *testing.T, operatorToken string) (*httptest.Server, *sessionapi.FileStore) {
	t.Helper()
	root := t.TempDir()
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeCloud)
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, store, sessionapi.NewBearerAuthorizer(store, operatorToken))
	return httptest.NewServer(mux), store
}

func writeAuthorizedSession(
	t *testing.T,
	root string,
	id string,
	kind sessionapi.SessionKind,
	parentID *string,
) (sessionapi.Manifest, string) {
	t.Helper()
	access, err := sessionapi.NewScopedToken(id, kind)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(root, id)
	specDir := filepath.Join(sessionDir, "specs", id)
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(specDir, "spec.md")
	if err := os.WriteFile(specPath, []byte("---\nversion: v0\nname: "+id+"\nplatform: cloud\n---\n# "+id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := sessionapi.ManifestFromInitial(sessionapi.InitialManifest{
		SessionID:       id,
		SessionKind:     kind,
		Runtime:         sessionapi.RuntimeCloud,
		CreatedAt:       "2026-05-18T12:00:00.000Z",
		Launcher:        "telosd",
		ParentSessionID: parentID,
		SessionSpecPath: &specPath,
		SpecName:        id,
		Access:          access,
		Specs: []sessionapi.InitialManifestSpec{{
			Index:           0,
			Name:            id,
			DirName:         id,
			SessionSpecPath: &specPath,
		}},
	})
	if err := sessionapi.WriteManifest(filepath.Join(sessionDir, "session.json"), &m); err != nil {
		t.Fatal(err)
	}
	return m, access.APIToken
}

func createSession(t *testing.T, baseURL string, body string) sessionapi.Session {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create session: expected 201, got %d: %s", resp.StatusCode, b)
	}
	var s sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&s)
	return s
}

func createSessionBody(t *testing.T, name string) string {
	t.Helper()
	return createSessionBodyWithConfig(t, name, "", "")
}

func createSessionBodyWithConfig(t *testing.T, name string, model string, thinking string) string {
	t.Helper()
	markdown := fmt.Sprintf("---\nversion: v0\nname: %s\nplatform: local\n---\n# %s\n", name, name)
	body, err := json.Marshal(sessionapi.SessionCreateRequest{
		SpecMarkdown: &markdown,
		Model:        model,
		Thinking:     thinking,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func getSession(t *testing.T, baseURL string, id string) sessionapi.Session {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&s)
	return s
}

func getSessionWithToken(t *testing.T, baseURL string, id string, token string) sessionapi.Session {
	t.Helper()
	req, err := http.NewRequest("GET", baseURL+"/api/sessions/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("get session: expected 200, got %d: %s", resp.StatusCode, body)
	}
	var s sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&s)
	return s
}

func stopSession(t *testing.T, baseURL string, id string) sessionapi.Session {
	t.Helper()
	req, _ := http.NewRequest("POST", baseURL+"/api/sessions/"+id+"/stop", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s sessionapi.Session
	json.NewDecoder(resp.Body).Decode(&s)
	return s
}

func post(t *testing.T, url string, body string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func assertEqual(t *testing.T, label string, expected, actual string) {
	t.Helper()
	if expected != actual {
		t.Errorf("%s: expected %q, got %q", label, expected, actual)
	}
}

func assertNonEmpty(t *testing.T, label string, value string) {
	t.Helper()
	if value == "" {
		t.Errorf("%s: expected non-empty string", label)
	}
}

func assertConfigStr(t *testing.T, config map[string]any, key string, expected string) {
	t.Helper()
	v, ok := config[key]
	if !ok {
		t.Errorf("config missing key %q", key)
		return
	}
	if s, ok := v.(string); !ok || s != expected {
		t.Errorf("config[%q]: expected %q, got %v", key, expected, v)
	}
}

func assertConfigFloat(t *testing.T, config map[string]any, key string, expected float64) {
	t.Helper()
	v, ok := config[key]
	if !ok {
		t.Errorf("config missing key %q", key)
		return
	}
	if f, ok := v.(float64); !ok || f != expected {
		t.Errorf("config[%q]: expected %v, got %v", key, expected, v)
	}
}

func intValueFromConfig(t *testing.T, config map[string]any, key string) int {
	t.Helper()
	v, ok := config[key]
	if !ok {
		t.Fatalf("config missing key %q", key)
	}
	switch value := v.(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		t.Fatalf("config[%q]: expected number, got %T %v", key, v, v)
		return 0
	}
}

func assertJSONType(t *testing.T, m map[string]any, key string, kind string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("missing key %q", key)
		return
	}
	switch kind {
	case "map":
		if _, ok := v.(map[string]any); !ok {
			t.Errorf("%q: expected object, got %T", key, v)
		}
	case "slice":
		if _, ok := v.([]any); !ok {
			t.Errorf("%q: expected array, got %T", key, v)
		}
	}
}

func diagnosticsHasErrorCode(errors []sessionapi.SessionErrorDiagnostics, code string) bool {
	for _, err := range errors {
		if err.ErrorCode == code {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
