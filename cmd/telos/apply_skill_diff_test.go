package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

type dryRunSkillFixture struct {
	path   string
	pkg    *spec.ApplyPackage
	digest string
	bundle []byte
}

func makeDryRunSkillFixture(t *testing.T) dryRunSkillFixture {
	t.Helper()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "verify-test")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: verify-test\n---\nVerify old behavior.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(path, []byte("---\nname: demo\nversion: 1.2.3\nskills: ['./verify-test*']\n---\n# Goal\nServe a demo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(path)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := spec.BuildApplyPackageWithSkillRefs(compiled, map[string]string{"verify-test": "@telos/verify-test:1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	digest, bundle, err := spec.BuildSkillBundle(compiled.Skills[0])
	if err != nil {
		t.Fatal(err)
	}
	return dryRunSkillFixture{path: path, pkg: pkg, digest: digest, bundle: bundle}
}

func TestDryRunLocalSkillIdentityAndOptionalContentDiff(t *testing.T) {
	fixture := makeDryRunSkillFixture(t)
	handlers := registryApplyHandlers(fixture.pkg)
	handlers["GET /api/account/bootstrap"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"personal_org_id":"org_personal","organizations":[{"id":"org_personal","default_publish_scope":"telos"}]}`))
	}
	metadata := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.SkillRecord{Scope: "telos", Name: "verify-test", Version: "1.0.0", Ref: "@telos/verify-test:1.0.0", Digest: fixture.digest})
	}
	handlers["GET /api/skills/telos/verify-test"] = metadata
	handlers["GET /api/skills/telos/verify-test/versions/1.0.0"] = metadata
	bundleRequests := 0
	unavailable := false
	handlers["GET /api/skills/telos/verify-test/versions/1.0.0/bundle"] = func(w http.ResponseWriter, r *http.Request) {
		bundleRequests++
		if unavailable {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(fixture.bundle)
	}
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	args := []string{fixture.path, "--goal", "goal_test"}
	preview := decodeApplyPreview(t, args...)
	if preview.Operation != "unchanged" || preview.Proposed.Digest != fixture.pkg.Digest || preview.Proposed.Skills[0].Ref != "@telos/verify-test:1.0.0" || !preview.Proposed.Skills[0].Starred {
		t.Fatalf("unchanged local skill preview: %+v", preview)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(fixture.path), "verify-test", "SKILL.md"), []byte("---\nname: verify-test\n---\nVerify new behavior.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preview = decodeApplyPreview(t, args...)
	if preview.Operation != "update" || preview.Proposed.Digest != "" || preview.Proposed.Skills[0].Ref != "" || bundleRequests != 0 {
		t.Fatalf("pending skill identity: %+v, bundle requests = %d", preview, bundleRequests)
	}
	preview = decodeApplyPreview(t, append(args, "--diff")...)
	if len(preview.SkillDiffs) != 1 || !strings.Contains(preview.SkillDiffs[0].Diff, "-Verify old behavior.") || !strings.Contains(preview.SkillDiffs[0].Diff, "+Verify new behavior.") || bundleRequests != 1 {
		t.Fatalf("skill contents: %+v, bundle requests = %d", preview.SkillDiffs, bundleRequests)
	}
	unavailable = true
	preview = decodeApplyPreview(t, append(args, "--diff")...)
	if preview.Operation != "update" || len(preview.Proposed.Skills) != 1 || !strings.Contains(strings.Join(preview.Notes, " "), "Deployed skill file diffs are unavailable") {
		t.Fatalf("unavailable old skill blocked the preview: %+v", preview)
	}
}

func TestDiffSkillFilesHandlesFileKindsAndModes(t *testing.T) {
	before := map[string]cloud.SkillFile{
		"removed.txt": {Mode: "0644", Data: []byte("old")},
		"script.sh":   {Mode: "0644", Data: []byte("run")},
		"unchanged":   {Mode: "0644", Data: []byte("same")},
	}
	after := map[string]cloud.SkillFile{
		"binary":    {Mode: "0644", Data: []byte{0, 1}},
		"empty":     {Mode: "0644"},
		"large":     {Mode: "0644", Data: []byte(strings.Repeat("x", 256*1024+1))},
		"script.sh": {Mode: "0755", Data: []byte("run")},
		"unchanged": {Mode: "0644", Data: []byte("same")},
	}
	diffs := diffSkillFiles("test", before, after)
	if len(diffs) != 5 || diffs[0].Path != "binary" || diffs[0].ContentOmitted != "binary content" || diffs[1].Path != "empty" || diffs[1].Change != "added" || diffs[2].ContentOmitted != "content exceeds 256 KiB" || diffs[3].Change != "removed" || diffs[4].OldMode != "0644" || diffs[4].NewMode != "0755" || diffs[4].Diff != "" {
		t.Fatalf("file changes = %+v", diffs)
	}
}

func TestDryRunRegistrySkillDiffHydratesBothRevisions(t *testing.T) {
	fixture := makeDryRunSkillFixture(t)
	if err := os.WriteFile(fixture.path, []byte(strings.ReplaceAll(string(mustReadFile(t, fixture.path)), "1.2.3", "1.2.4")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(fixture.path), "verify-test", "SKILL.md"), []byte("---\nname: verify-test\n---\nVerify new behavior.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := spec.BuildApplyPackageWithSkillRefs(compiled, map[string]string{"verify-test": "@telos/verify-test:2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	newDigest, newBundle, err := spec.BuildSkillBundle(compiled.Skills[0])
	if err != nil {
		t.Fatal(err)
	}
	handlers := registryApplyHandlers(fixture.pkg)
	handlers["GET /api/packages/telos/demo/versions/1.2.4"] = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.PackageVersionRecord{Scope: "telos", Name: "demo", Version: "1.2.4", Ref: "@telos/demo:1.2.4", Digest: proposed.Digest})
	}
	handlers["GET /api/packages/telos/demo/versions/1.2.4/bundle"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(proposed.Bytes)
	}
	for _, revision := range []struct {
		version, digest string
		bundle          []byte
	}{
		{"1.0.0", fixture.digest, fixture.bundle}, {"2.0.0", newDigest, newBundle},
	} {
		handlers["GET /api/skills/telos/verify-test/versions/"+revision.version] = func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(cloud.SkillRecord{Digest: revision.digest})
		}
		handlers["GET /api/skills/telos/verify-test/versions/"+revision.version+"/bundle"] = func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(revision.bundle)
		}
	}
	server := inferenceTestServer(t, handlers)
	defer server.Close()
	configureDryRunTest(t, server.URL)
	preview := decodeApplyPreview(t, "@telos/demo:1.2.4", "--goal", "goal_test", "--diff")
	if preview.Operation != "update" || len(preview.SkillDiffs) != 1 || !strings.Contains(preview.SkillDiffs[0].Diff, "+Verify new behavior.") || !strings.Contains(preview.SpecDiff, "+version: 1.2.4") {
		t.Fatalf("registry revision diff: %+v", preview)
	}
}

func TestDiffFlagRequiresDryRun(t *testing.T) {
	stdout, stderr, err := inferenceCLIProcess(t, "apply", "missing.md", "--diff")
	if err == nil || stdout != "" || !strings.Contains(stderr, "--diff requires --dry-run") {
		t.Fatalf("invalid diff flag: error=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
}
