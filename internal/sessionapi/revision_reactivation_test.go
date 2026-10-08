package sessionapi_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/spec"
)

func TestApplySessionSpecReactivatesHistoricalPackages(t *testing.T) {
	store := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store.PackageRoot = t.TempDir()
	first := writeTestApplyPackage(t, store.PackageRoot, "probe", "alpha", "0.1.0")
	second := writeTestApplyPackage(t, store.PackageRoot, "probe", "beta", "0.1.1")
	var events []sessionapi.SpecUpdateEvent
	store.OnSpecUpdate = func(event sessionapi.SpecUpdateEvent) { events = append(events, event) }
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, store, sessionapi.AllowAllAuthorizer{}, sessionapi.RuntimeIdentity{})
	server := httptest.NewServer(mux)
	defer server.Close()
	apply := func(pkg *spec.ApplyPackage) *sessionapi.SessionSpecUpdateResponse {
		t.Helper()
		body, err := json.Marshal(sessionapi.SessionSpecUpdateRequest{PackageDigest: pkg.Digest})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPut, server.URL+"/api/sessions/probe/spec", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("PUT spec: HTTP %d: %s", response.StatusCode, data)
		}
		var result sessionapi.SessionSpecUpdateResponse
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return &result
	}
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	saved := map[string]string{}
	var previousSpecPath, previousDigest, previousRevision, manifestPath string
	for i, pkg := range []*spec.ApplyPackage{first, second, first, second} {
		sequence := i + 1
		version, skill := "0.1.0", "alpha"
		if pkg == second {
			version, skill = "0.1.1", "beta"
		}
		result := apply(pkg)
		operation := "updated"
		if sequence == 1 {
			operation = "created"
		}
		if result.Operation != operation || result.Session == nil {
			t.Fatalf("activation %d: %#v", sequence, result)
		}
		manifestPath = filepath.Join(store.Root, result.Session.SessionID, "session.json")
		manifest, err := sessionapi.ReadManifest(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if *manifest.CurrentSpecVersion != sequence || len(manifest.SpecVersions) != sequence || *manifest.CurrentRevision != version || *manifest.PackageDigest != pkg.Digest {
			t.Fatalf("activation %d did not advance history: %#v", sequence, manifest)
		}
		entry := manifest.SpecVersions[sequence-1]
		specPath := entry["spec_path"].(string)
		expectedSpec, _, err := spec.ApplyPackageSpec(pkg.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{specPath, *manifest.SessionSpecPath, *manifest.SourceSpecPath, filepath.Join(filepath.Dir(manifestPath), "package", "SPEC.md")} {
			if read(path) != string(expectedSpec) {
				t.Fatalf("activation %d: incorrect spec at %s", sequence, path)
			}
		}
		activeSkill := filepath.Join(filepath.Dir(manifestPath), "package", "skills", skill, "SKILL.md")
		if !strings.Contains(read(activeSkill), "Use "+skill+".") {
			t.Fatalf("activation %d did not switch the skill package", sequence)
		}
		if result.Session.Reconciliation == nil || result.Session.Reconciliation.State != sessionapi.ReconciliationPending {
			t.Fatalf("activation %d reused old acceptance: %#v", sequence, result.Session.Reconciliation)
		}
		if len(events) != i {
			t.Fatalf("activation %d emitted %d updates", sequence, len(events))
		}
		for path, contents := range saved {
			if read(path) != contents {
				t.Fatalf("activation %d changed historical file %s", sequence, path)
			}
		}
		if sequence > 1 {
			event := events[i-1]
			if event.PreviousSpecVersion != sequence-1 || event.CurrentSpecVersion != sequence || event.PreviousRevision != previousRevision || event.CurrentRevision != version || event.PreviousPackageDigest != previousDigest || event.CurrentPackageDigest != pkg.Digest {
				t.Fatalf("activation %d: incorrect update event: %#v", sequence, event)
			}
			if event.PreviousSpecPath != previousSpecPath || event.CurrentSpecPath != specPath || entry["previous_revision"] != previousRevision {
				t.Fatalf("activation %d: incorrect predecessor: %#v", sequence, entry)
			}
			diff := read(event.DiffPath)
			if !strings.Contains(diff, "-version: "+previousRevision) || !strings.Contains(diff, "+version: "+version) {
				t.Fatalf("activation %d has the wrong transition diff:\n%s", sequence, diff)
			}
			saved[event.DiffPath] = diff
		}
		for _, path := range []string{specPath, *manifest.SourceSpecPath, filepath.Join(filepath.Dir(specPath), "revision.json")} {
			saved[path] = read(path)
		}
		if sequence == 1 {
			completed, reason, finished, yes := "completed", "verifier_conceded", "2026-10-08T00:00:00Z", true
			manifest.Epochs = append(manifest.Epochs, sessionapi.Epoch{
				ID: 1, SpecVersion: manifest.CurrentSpecVersion, PackageDigest: manifest.PackageDigest,
				FinishedAt: &finished, Result: &completed, CompletionReason: &reason,
				VerifierConceded: &yes, CheckpointSaved: &yes,
			})
			if err := sessionapi.WriteManifest(manifestPath, manifest); err != nil {
				t.Fatal(err)
			}
		}
		previousSpecPath, previousDigest, previousRevision = specPath, pkg.Digest, version
	}
	before := read(manifestPath)
	if result := apply(second); result.Operation != "unchanged" {
		t.Fatalf("current package retry: %#v", result)
	}
	if len(events) != 3 || read(manifestPath) != before {
		t.Fatal("current package retry changed the manifest or emitted an update")
	}
}

func TestReactivationUsesIncomingPackageWhenHistoricalFilesAreMissing(t *testing.T) {
	store := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store.PackageRoot = t.TempDir()
	first := writeTestApplyPackage(t, store.PackageRoot, "probe", "alpha", "0.1.0")
	second := writeTestApplyPackage(t, store.PackageRoot, "probe", "beta", "0.1.1")
	initial, err := store.UpdateSpec("probe", sessionapi.SessionSpecUpdateRequest{PackageDigest: first.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSpec("probe", sessionapi.SessionSpecUpdateRequest{PackageDigest: second.Digest}); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(store.Root, initial.Session.SessionID, "revisions", "0.1.0")
	if err := os.RemoveAll(filepath.Join(old, "package")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "SPEC.md"), []byte("damaged historical spec"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := store.UpdateSpec("probe", sessionapi.SessionSpecUpdateRequest{PackageDigest: first.Digest})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(*result.Session.SessionSpecPath)
	if err != nil {
		t.Fatal(err)
	}
	expected, _, err := spec.ApplyPackageSpec(first.Bytes)
	if err != nil || string(data) != string(expected) {
		t.Fatalf("reactivation did not use the verified incoming package: %s, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(old, "package")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reactivation rewrote historical package: %v", err)
	}
}

func TestReactivationRejectsHistoricalPackageContentChanges(t *testing.T) {
	store := sessionapi.NewFileStore(t.TempDir(), sessionapi.RuntimeCloud)
	store.PackageRoot = t.TempDir()
	first := writeTestApplyPackage(t, store.PackageRoot, "probe", "alpha", "0.1.0")
	second := writeTestApplyPackage(t, store.PackageRoot, "probe", "beta", "0.1.1")
	for _, pkg := range []*spec.ApplyPackage{first, second} {
		if _, err := store.UpdateSpec("probe", sessionapi.SessionSpecUpdateRequest{PackageDigest: pkg.Digest}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if _, err := spec.ExtractApplyPackage(first.Bytes, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "alpha", "SKILL.md"), []byte("---\nname: alpha\n---\nChanged alpha."), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironmentWithBase(filepath.Join(dir, "SPEC.md"), dir)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := spec.BuildApplyPackage(compiled)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Manifest.Spec.Digest != first.Manifest.Spec.Digest || changed.Digest == first.Digest {
		t.Fatal("test requires identical spec and different skill contents")
	}
	path := filepath.Join(t.TempDir(), "changed.tar.gz")
	if err := os.WriteFile(path, changed.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateSpec("probe", sessionapi.SessionSpecUpdateRequest{PackagePath: path, PackageDigest: changed.Digest})
	if !errors.Is(err, sessionapi.ErrConflict) {
		t.Fatalf("changed historical package must conflict: %v", err)
	}
	changedSpec := "---\nversion: 0.1.0\nname: probe\nplatform: cloud\n---\nChanged spec.\n"
	_, err = store.UpdateSpec("probe", sessionapi.SessionSpecUpdateRequest{SpecMarkdown: changedSpec})
	if !errors.Is(err, sessionapi.ErrConflict) {
		t.Fatalf("changed historical spec must conflict: %v", err)
	}
}
