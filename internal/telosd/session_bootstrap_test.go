package telosd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

type fakeReconcileStore struct {
	sessions []sessionapi.Session
	creates  []sessionapi.SessionCreateRequest
	stops    []string
}

func (s *fakeReconcileStore) Create(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	s.creates = append(s.creates, req)
	name := "auth"
	kind := sessionapi.KindController
	session := sessionapi.Session{
		SessionID:   "sess_created",
		SessionKind: &kind,
		SpecName:    &name,
		Status:      sessionapi.StatusRunning,
		Provenance: map[string]any{
			"cloud_session_id":   req.CloudSessionID,
			"cloud_session_name": req.CloudSessionName,
		},
		SpecVersions: []map[string]any{{
			"package_digest": req.PackageDigest,
		}},
	}
	s.sessions = append(s.sessions, session)
	return &session, nil
}

func (s *fakeReconcileStore) List() ([]sessionapi.Session, error) {
	return append([]sessionapi.Session{}, s.sessions...), nil
}

func (s *fakeReconcileStore) Stop(id string) (*sessionapi.Session, error) {
	s.stops = append(s.stops, id)
	for i := range s.sessions {
		if s.sessions[i].SessionID == id {
			s.sessions[i].Status = sessionapi.StatusStopped
			return &s.sessions[i], nil
		}
	}
	return &sessionapi.Session{SessionID: id, Status: sessionapi.StatusStopped}, nil
}

func (s *fakeReconcileStore) Spec(string) (*sessionapi.SessionSpecResponse, error) {
	return nil, sessionapi.ErrNotFound
}
func (s *fakeReconcileStore) UpdateSpec(string, sessionapi.SessionSpecUpdateRequest) (*sessionapi.SessionSpecUpdateResponse, error) {
	return nil, sessionapi.ErrNotFound
}
func (s *fakeReconcileStore) Get(string) (*sessionapi.Session, error) {
	return nil, sessionapi.ErrNotFound
}
func (s *fakeReconcileStore) Transcript(string) (string, error) {
	return "", sessionapi.ErrNotFound
}
func (s *fakeReconcileStore) Events(string) ([]sessionapi.SessionEvent, error) {
	return nil, sessionapi.ErrNotFound
}

func TestSessionBootstrapReconcilerCreatesDesiredPackageSession(t *testing.T) {
	pkg := buildMaterializerTestPackage(t, "auth")
	digest := pkg.Digest
	root := t.TempDir()
	packagePath := filepath.Join(root, "blobs", "sha256", digest[len("sha256:"):], "package.tar.gz")
	if err := writePackageCacheEntry(packagePath, pkg.Bytes); err != nil {
		t.Fatal(err)
	}

	store := &fakeReconcileStore{}
	reconciler := sessionBootstrapReconciler{
		packageRoot: root,
		store:       store,
	}

	if err := reconciler.reconcile([]cloudBootstrapSession{{
		CloudSessionID: "sess_123",
		Name:           "auth",
		PackageDigest:  digest,
	}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(store.creates) != 1 {
		t.Fatalf("creates = %#v", store.creates)
	}
	if store.creates[0].PackagePath != packagePath {
		t.Fatalf("PackagePath = %q want %q", store.creates[0].PackagePath, packagePath)
	}
	if store.creates[0].PackageDigest != digest {
		t.Fatalf("PackageDigest = %q want %q", store.creates[0].PackageDigest, digest)
	}
	if store.creates[0].CloudSessionID != "sess_123" {
		t.Fatalf("CloudSessionID = %q want sess_123", store.creates[0].CloudSessionID)
	}
	if store.creates[0].CloudSessionName != "auth" {
		t.Fatalf("CloudSessionName = %q want auth", store.creates[0].CloudSessionName)
	}
	if store.creates[0].Model != fallbackCloudSessionModel {
		t.Fatalf("Model = %q want %q", store.creates[0].Model, fallbackCloudSessionModel)
	}
	if store.creates[0].AgentTimeoutSec != nil {
		t.Fatalf("AgentTimeoutSec should not default for controller bootstrap: %v", store.creates[0].AgentTimeoutSec)
	}
}

func TestSessionBootstrapMatchesCloudSessionNameBeforeSpecName(t *testing.T) {
	digest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	specName := "auth-v2"
	store := &fakeReconcileStore{sessions: []sessionapi.Session{{
		SessionID: "sess_existing",
		SpecName:  &specName,
		Status:    sessionapi.StatusRunning,
		Provenance: map[string]any{
			"cloud_session_name": "auth",
		},
		SpecVersions: []map[string]any{{
			"package_digest": digest,
		}},
	}}}

	reconciler := sessionBootstrapReconciler{
		packageRoot: t.TempDir(),
		store:       store,
	}

	if err := reconciler.reconcile([]cloudBootstrapSession{{
		Name:          "auth",
		PackageDigest: digest,
	}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(store.creates) != 0 || len(store.stops) != 0 {
		t.Fatalf("expected no changes, creates=%#v stops=%#v", store.creates, store.stops)
	}
}

func TestCloudSessionModelUsesEnvOverride(t *testing.T) {
	t.Setenv("TELOS_CLOUD_DEFAULT_MODEL", "sail-research/custom")

	if got := cloudSessionModel(); got != "sail-research/custom" {
		t.Fatalf("cloudSessionModel = %q", got)
	}
}

func TestCloudSessionThinkingUsesEnvOverride(t *testing.T) {
	t.Setenv("TELOS_CLOUD_DEFAULT_THINKING", "low")

	if got := cloudSessionThinking(); got != "low" {
		t.Fatalf("cloudSessionThinking = %q", got)
	}
}

func TestCloudAgentTimeoutUsesEnvOverride(t *testing.T) {
	t.Setenv("TELOS_AGENT_TIMEOUT_SEC", "120")

	got := cloudAgentTimeoutSec()
	if got == nil || *got != 120 {
		t.Fatalf("cloudAgentTimeoutSec = %v want 120", got)
	}
}

func TestCloudAgentTimeoutCanBeDisabled(t *testing.T) {
	t.Setenv("TELOS_AGENT_TIMEOUT_SEC", "0")

	got := cloudAgentTimeoutSec()
	if got == nil || *got != 0 {
		t.Fatalf("cloudAgentTimeoutSec = %v want 0", got)
	}
}

func TestCloudAgentTimeoutDefaultsUnset(t *testing.T) {
	if got := cloudAgentTimeoutSec(); got != nil {
		t.Fatalf("cloudAgentTimeoutSec = %v want nil", got)
	}
}

func TestSessionBootstrapDesiredSession(t *testing.T) {
	t.Setenv("TELOS_SESSION_ID", "sess_123")
	t.Setenv("TELOS_SESSION_NAME", "auth")
	t.Setenv("TELOS_PACKAGE_DIGEST", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	session, ok := bootstrapSessionFromEnv()

	if !ok {
		t.Fatal("expected session bootstrap")
	}
	if session.Name != "auth" {
		t.Fatalf("Name = %q", session.Name)
	}
	if session.CloudSessionID != "sess_123" {
		t.Fatalf("CloudSessionID = %q", session.CloudSessionID)
	}
	if session.PackageDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("PackageDigest = %q", session.PackageDigest)
	}
}

func TestSessionBootstrapCanBeDisabled(t *testing.T) {
	t.Setenv("TELOS_SESSION_BOOTSTRAP_ENABLED", "0")
	t.Setenv("TELOS_SESSION_NAME", "auth")
	t.Setenv("TELOS_PACKAGE_DIGEST", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("TELOS_PACKAGE_ROOT", t.TempDir())
	store := &fakeReconcileStore{}

	startSessionBootstrapReconciler(context.Background(), store, nil)

	if len(store.creates) != 0 || len(store.stops) != 0 {
		t.Fatalf("expected disabled bootstrap to do nothing, creates=%#v stops=%#v", store.creates, store.stops)
	}
}

func TestSessionBootstrapNoopsWhenDigestMatches(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	name := "auth"
	store := &fakeReconcileStore{sessions: []sessionapi.Session{{
		SessionID: "sess_existing",
		SpecName:  &name,
		Status:    sessionapi.StatusRunning,
		SpecVersions: []map[string]any{{
			"package_digest": digest,
		}},
	}}}

	reconciler := sessionBootstrapReconciler{
		packageRoot: t.TempDir(),
		store:       store,
	}

	if err := reconciler.reconcile([]cloudBootstrapSession{{
		Name:          name,
		PackageDigest: digest,
	}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(store.creates) != 0 || len(store.stops) != 0 {
		t.Fatalf("expected no changes, creates=%#v stops=%#v", store.creates, store.stops)
	}
}

func TestSessionBootstrapDoesNotOverwriteUpdatedPackageDigest(t *testing.T) {
	initialDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	updatedDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	name := "auth"
	store := &fakeReconcileStore{sessions: []sessionapi.Session{{
		SessionID: "sess_existing",
		SpecName:  &name,
		Status:    sessionapi.StatusRunning,
		SpecVersions: []map[string]any{{
			"package_digest": updatedDigest,
		}},
	}}}

	reconciler := sessionBootstrapReconciler{
		packageRoot: t.TempDir(),
		store:       store,
	}

	if err := reconciler.reconcile([]cloudBootstrapSession{{
		Name:          name,
		PackageDigest: initialDigest,
	}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(store.creates) != 0 || len(store.stops) != 0 {
		t.Fatalf("expected bootstrap seed to leave updated session alone, creates=%#v stops=%#v", store.creates, store.stops)
	}
}
