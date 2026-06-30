package telosd

import (
	"os"
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
			"deployment_id":   req.DeploymentID,
			"deployment_name": req.DeploymentName,
		},
		SpecVersions: []map[string]any{{
			"apply_package_digest": req.ApplyPackageDigest,
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
func (s *fakeReconcileStore) WorkspacePath(string, string) (string, error) {
	return "", sessionapi.ErrNotFound
}

func TestDeploymentBootstrapReconcilerCreatesDesiredPackageSession(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root := t.TempDir()
	packagePath := filepath.Join(root, "blobs", "sha256", digest[len("sha256:"):], "package.tar.gz")
	if err := os.MkdirAll(filepath.Dir(packagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packagePath, []byte("package"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &fakeReconcileStore{}
	reconciler := deploymentBootstrapReconciler{
		packageRoot: root,
		store:       store,
	}

	if err := reconciler.reconcile([]deploymentSession{{
		DeploymentID:  "dep_123",
		Name:          "auth",
		PackageDigest: digest,
	}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(store.creates) != 1 {
		t.Fatalf("creates = %#v", store.creates)
	}
	if store.creates[0].ApplyPackagePath != packagePath {
		t.Fatalf("ApplyPackagePath = %q want %q", store.creates[0].ApplyPackagePath, packagePath)
	}
	if store.creates[0].ApplyPackageDigest != digest {
		t.Fatalf("ApplyPackageDigest = %q want %q", store.creates[0].ApplyPackageDigest, digest)
	}
	if store.creates[0].DeploymentID != "dep_123" {
		t.Fatalf("DeploymentID = %q want dep_123", store.creates[0].DeploymentID)
	}
	if store.creates[0].DeploymentName != "auth" {
		t.Fatalf("DeploymentName = %q want auth", store.creates[0].DeploymentName)
	}
	if store.creates[0].Model != defaultCloudSessionModel {
		t.Fatalf("Model = %q want %q", store.creates[0].Model, defaultCloudSessionModel)
	}
}

func TestControlSessionReconcilerMatchesDeploymentNameBeforeSpecName(t *testing.T) {
	digest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	specName := "auth-v2"
	store := &fakeReconcileStore{sessions: []sessionapi.Session{{
		SessionID: "sess_existing",
		SpecName:  &specName,
		Status:    sessionapi.StatusRunning,
		Provenance: map[string]any{
			"deployment_name": "auth",
		},
		SpecVersions: []map[string]any{{
			"apply_package_digest": digest,
		}},
	}}}

	reconciler := deploymentBootstrapReconciler{
		packageRoot: t.TempDir(),
		store:       store,
	}

	if err := reconciler.reconcile([]deploymentSession{{
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

func TestDeploymentBootstrapDesiredSession(t *testing.T) {
	t.Setenv("TELOS_DEPLOYMENT_ID", "dep_123")
	t.Setenv("TELOS_DEPLOYMENT_NAME", "auth")
	t.Setenv("TELOS_PACKAGE_DIGEST", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	session, ok := deploymentBootstrapSession()

	if !ok {
		t.Fatal("expected deployment bootstrap")
	}
	if session.Name != "auth" {
		t.Fatalf("Name = %q", session.Name)
	}
	if session.DeploymentID != "dep_123" {
		t.Fatalf("DeploymentID = %q", session.DeploymentID)
	}
	if session.PackageDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("PackageDigest = %q", session.PackageDigest)
	}
}

func TestControlSessionReconcilerNoopsWhenDigestMatches(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	name := "auth"
	store := &fakeReconcileStore{sessions: []sessionapi.Session{{
		SessionID: "sess_existing",
		SpecName:  &name,
		Status:    sessionapi.StatusRunning,
		SpecVersions: []map[string]any{{
			"apply_package_digest": digest,
		}},
	}}}

	reconciler := deploymentBootstrapReconciler{
		packageRoot: t.TempDir(),
		store:       store,
	}

	if err := reconciler.reconcile([]deploymentSession{{
		Name:          name,
		PackageDigest: digest,
	}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(store.creates) != 0 || len(store.stops) != 0 {
		t.Fatalf("expected no changes, creates=%#v stops=%#v", store.creates, store.stops)
	}
}
