package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/spec"
)

func revisionTestPackage(t *testing.T, goal, ref string, files map[string]spec.ApplyPackageFileContents) (*spec.ApplyPackage, []byte) {
	t.Helper()
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills", "check")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: check\ndescription: Check the demo.\n---\nCheck the result.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, file := range files {
		path := filepath.Join(skillRoot, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, file.Mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, file.Mode); err != nil {
			t.Fatal(err)
		}
	}
	specPath := filepath.Join(root, "SPEC.md")
	if err := os.WriteFile(specPath, []byte("---\nname: demo\nversion: 1.0.0\nplatform: cloud\nskills: [./skills/check]\n---\n# Goal\n"+goal+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiled, err := spec.CompileEnvironment(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var refs map[string]string
	if ref != "" {
		refs = map[string]string{"check": ref}
	}
	pkg, err := spec.BuildApplyPackageWithSkillRefs(compiled, refs)
	if err != nil {
		t.Fatal(err)
	}
	_, skillBundle, err := spec.BuildSkillBundle(compiled.Skills[0])
	if err != nil {
		t.Fatal(err)
	}
	return pkg, skillBundle
}

func TestRevisionDiffCLIUsesHistoricalPackagesAndDefaultDirection(t *testing.T) {
	oldPackage, _ := revisionTestPackage(t, "Serve the old demo.", "", nil)
	newPackage, _ := revisionTestPackage(t, "Serve the new demo.", "", nil)
	oldRevision, newRevision := testRevision(7), testRevision(12)
	oldRevision.PackageDigest, newRevision.PackageDigest = oldPackage.Digest, newPackage.Digest
	revisionTestServer(t, map[string]http.HandlerFunc{
		"GET " + revisionTestPath: func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(cloud.RevisionPage{CurrentRevisionID: "rev_12", Revisions: []cloud.Revision{newRevision, oldRevision}})
		},
		"GET " + revisionTestPath + "/rev_12": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(newRevision)
		},
		"GET " + revisionTestPath + "/rev_7/bundle":  func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(oldPackage.Bytes) },
		"GET " + revisionTestPath + "/rev_12/bundle": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(newPackage.Bytes) },
	})
	local := t.TempDir()
	t.Chdir(local)
	if err := os.WriteFile("SPEC.md", []byte("Unrelated local changes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runRevisionCLI(t, "diff", "sess_test", "7", "--json")
	var result revisionDiffResult
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &result) != nil || result.From.Sequence != 7 || result.To.Sequence != 12 || len(result.Files) != 1 || result.Files[0].Path != "SPEC.md" {
		t.Fatalf("diff: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	patch := result.Files[0].Patch
	if !strings.Contains(patch, "-Serve the old demo.") || !strings.Contains(patch, "+Serve the new demo.") {
		t.Fatalf("wrong default direction:\n%s", patch)
	}
	stdout, stderr, code = runRevisionCLI(t, "diff", "sess_test", "12", "7")
	if code != 0 || !strings.Contains(stdout, "-Serve the new demo.") || !strings.Contains(stdout, "+Serve the old demo.") {
		t.Fatalf("explicit diff: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	data, err := os.ReadFile("SPEC.md")
	if err != nil || string(data) != "Unrelated local changes\n" {
		t.Fatalf("diff changed local SPEC: %s, %v", data, err)
	}
}

func TestRevisionDiffIncludesSkillContentsModesAndDependencies(t *testing.T) {
	for _, referenced := range []bool{false, true} {
		t.Run(fmt.Sprintf("referenced=%t", referenced), func(t *testing.T) {
			ref := ""
			if referenced {
				ref = "@team/check:1.0.0"
			}
			oldPackage, oldSkill := revisionTestPackage(t, "Serve the demo.", ref, map[string]spec.ApplyPackageFileContents{
				"run.sh":      {Data: []byte("echo check\n"), Mode: 0o644},
				"removed.txt": {Data: []byte("old\n"), Mode: 0o644},
				"binary":      {Data: []byte{0, 1}, Mode: 0o644},
				"large.txt":   {Data: []byte(strings.Repeat("old\n", 6000)), Mode: 0o644},
			})
			newPackage, newSkill := revisionTestPackage(t, "Serve the demo.", ref, map[string]spec.ApplyPackageFileContents{
				"run.sh":    {Data: []byte("echo check\n"), Mode: 0o755},
				"empty.txt": {Data: []byte{}, Mode: 0o644},
				"binary":    {Data: []byte{0, 2}, Mode: 0o644},
				"large.txt": {Data: []byte(strings.Repeat("new\n", 6000)), Mode: 0o644},
			})
			var skillReads atomic.Int32
			routes := map[string]http.HandlerFunc{
				"GET " + revisionTestPath + "/rev_7/bundle":  func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(oldPackage.Bytes) },
				"GET " + revisionTestPath + "/rev_12/bundle": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(newPackage.Bytes) },
			}
			for _, bundle := range []struct {
				id    string
				pkg   *spec.ApplyPackage
				skill []byte
			}{{"rev_7", oldPackage, oldSkill}, {"rev_12", newPackage, newSkill}} {
				routes["GET "+revisionTestPath+"/"+bundle.id+"/skills/team/check/1.0.0/bundle"] = func(w http.ResponseWriter, r *http.Request) {
					skillReads.Add(1)
					if r.URL.Query().Get("digest") != bundle.pkg.Manifest.Skills["check"].Digest {
						t.Errorf("historical skill digest was not pinned: %s", r.URL)
					}
					_, _ = w.Write(bundle.skill)
				}
			}
			server := revisionTestServer(t, routes)
			oldRevision, newRevision := testRevision(7), testRevision(12)
			oldRevision.PackageDigest, newRevision.PackageDigest = oldPackage.Digest, newPackage.Digest
			result, err := compareRevisions(context.Background(), cloud.NewClient(server.URL, "control-token"), "sess_test", &oldRevision, &newRevision)
			if err != nil {
				t.Fatal(err)
			}
			changes := map[string]revisionFileDiff{}
			for _, change := range result.Files {
				changes[change.Path] = change
			}
			mode := changes["skills/check/run.sh"]
			if len(changes) != 5 || mode.OldMode != "0644" || mode.NewMode != "0755" || changes["skills/check/removed.txt"].Status != "deleted" || changes["skills/check/empty.txt"].Status != "added" || changes["skills/check/binary"].Notice != "binary content differs" || !strings.Contains(changes["skills/check/large.txt"].Notice, "too large") {
				t.Fatalf("missing file changes: %+v", changes)
			}
			if len(result.Skills) != 1 || result.Skills[0].Before.Digest == result.Skills[0].After.Digest || (referenced && skillReads.Load() != 2) || (!referenced && skillReads.Load() != 0) {
				t.Fatalf("skill changes=%+v reads=%d", result.Skills, skillReads.Load())
			}
		})
	}
}

func TestRevisionDiffRejectsWrongDigestAndSkipsIdenticalPackages(t *testing.T) {
	pkg := testApplyPackage(t)
	var reads atomic.Int32
	server := revisionTestServer(t, map[string]http.HandlerFunc{
		"GET " + revisionTestPath + "/rev_7/bundle": func(w http.ResponseWriter, r *http.Request) {
			reads.Add(1)
			_, _ = w.Write(pkg.Bytes)
		},
	})
	client := cloud.NewClient(server.URL, "control-token")
	oldRevision, newRevision := testRevision(7), testRevision(12)
	result, err := compareRevisions(context.Background(), client, "sess_test", &oldRevision, &newRevision)
	if err != nil || len(result.Files) != 0 || reads.Load() != 0 {
		t.Fatalf("identical packages: %+v %v reads=%d", result, err, reads.Load())
	}
	newRevision.PackageDigest = pkg.Digest
	if _, err := compareRevisions(context.Background(), client, "sess_test", &oldRevision, &newRevision); err == nil || !strings.Contains(err.Error(), "digest mismatch") || reads.Load() != 1 {
		t.Fatalf("unverified historical package accepted: %v reads=%d", err, reads.Load())
	}
}
