package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func serveInstallationUpdate(t *testing.T, version string, binary, skill []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/"+version+"/") && r.URL.Path != "/latest/manifest.json" {
			t.Errorf("request was not pinned to the selected release: %s", r.URL.Path)
		}
		switch filepath.Base(r.URL.Path) {
		case "manifest.json":
			fmt.Fprint(w, updateTestManifest(version))
		case "SHA256SUMS":
			fmt.Fprint(w, updateTestSums(binary, skill))
		case "telos-" + runtime.GOOS + "-" + runtime.GOARCH, "telosd-" + runtime.GOOS + "-" + runtime.GOARCH:
			w.Write(binary)
		case "telos-cli-skill.tar.gz":
			w.Write(skill)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestInstallationUpdateRepairsCompanionsAtCurrentVersion(t *testing.T) {
	dir := t.TempDir()
	skillsDir := t.TempDir()
	t.Setenv("TELOS_AGENT_SKILLS_DIR", skillsDir)
	binary := []byte("current CLI")
	target := filepath.Join(dir, "telos")
	if err := os.WriteFile(target, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(target)
	if err := os.WriteFile(filepath.Join(dir, "telosd"), []byte("old runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillTarget := filepath.Join(skillsDir, "telos-cli")
	if err := os.MkdirAll(skillTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillTarget, "obsolete.md"), []byte("obsolete"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := serveInstallationUpdate(t, "v0.1.5", binary, updateTestSkill(t))
	result, err := updateCLI(target, "v0.1.5", "latest", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Components, ",") != "telosd,telos-cli skill" {
		t.Fatalf("updated components = %v", result.Components)
	}
	after, _ := os.Stat(target)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged CLI was replaced")
	}
	if _, err := os.Stat(filepath.Join(skillTarget, "obsolete.md")); !os.IsNotExist(err) {
		t.Fatal("obsolete skill content remains")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "telosd")); err != nil || !bytes.Equal(data, binary) {
		t.Fatalf("runtime = %q, %v", data, err)
	}
	second, err := updateCLI(target, "v0.1.5", "latest", server.URL, server.Client())
	if err != nil || len(second.Components) != 0 {
		t.Fatalf("repeated update = %+v, %v", second, err)
	}
	assertNoUpdateStage(t, dir)
	assertNoUpdateStage(t, skillsDir)
}

func TestInstallationUpdateRemembersSkillDirectoryAndPreservesSymlinks(t *testing.T) {
	dir := t.TempDir()
	skillsDir := t.TempDir()
	t.Setenv("TELOS_AGENT_SKILLS_DIR", "")
	target := filepath.Join(dir, "telos")
	if err := os.WriteFile(target, []byte("old CLI"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeTarget := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(runtimeTarget, []byte("old runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeLink := filepath.Join(dir, "telosd")
	if err := os.Symlink(runtimeTarget, runtimeLink); err != nil {
		t.Fatal(err)
	}
	skillTarget := filepath.Join(t.TempDir(), "custom-skill")
	if err := os.MkdirAll(skillTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	skillLink := filepath.Join(skillsDir, "telos-cli")
	if err := os.Symlink(skillTarget, skillLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, installedSkillPathFile), []byte(skillLink+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := serveInstallationUpdate(t, "v0.1.5", []byte("new binary"), updateTestSkill(t))
	for _, current := range []string{"v0.1.4", "v0.1.5"} {
		if _, err := updateCLI(target, current, "latest", server.URL, server.Client()); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []string{runtimeLink, skillLink} {
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink changed: %s (%v)", link, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(skillTarget, "SKILL.md")); err != nil || string(data) != "released SKILL.md" {
		t.Fatalf("skill = %q, %v", data, err)
	}
	if data, err := os.ReadFile(runtimeTarget); err != nil || string(data) != "new binary" {
		t.Fatalf("runtime = %q, %v", data, err)
	}
}

func TestInstallationUpdateCloudClientKeepsRuntimeAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TELOS_AGENT_SKILLS_DIR", t.TempDir())
	target := filepath.Join(dir, "telos")
	if err := os.WriteFile(target, []byte("old CLI"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := serveInstallationUpdate(t, "v0.1.5", []byte("new CLI"), updateTestSkill(t))
	if _, err := updateCLI(target, "v0.1.4", "latest", server.URL, server.Client()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "telosd")); !os.IsNotExist(err) {
		t.Fatal("Cloud client update installed a runtime")
	}
}

func TestInstallationUpdateReplacementFailures(t *testing.T) {
	setup := func(t *testing.T) (dir, daemon, skill, cli string) {
		dir = t.TempDir()
		daemon = filepath.Join(dir, "telosd")
		skill = filepath.Join(dir, "telos-cli")
		cli = filepath.Join(dir, "telos")
		for path, data := range map[string]string{daemon: "old runtime", cli: "old CLI", filepath.Join(skill, "SKILL.md"): "old skill"} {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return dir, daemon, skill, cli
	}
	stageFile := func(t *testing.T, path, data string) string {
		if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	assertFile := func(t *testing.T, path, want string) {
		t.Helper()
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", path, data, err, want)
		}
	}
	assertNoPrevious := func(t *testing.T, dir string) {
		t.Helper()
		if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.previous")); len(leftovers) != 0 {
			t.Fatalf("left previous copies behind: %v", leftovers)
		}
	}

	t.Run("a failed skill swap restores the previous skill", func(t *testing.T) {
		dir, daemon, skill, cli := setup(t)
		err := installReleaseReplacements([]*releaseReplacement{
			{component: "telos-cli skill", target: skill, stage: filepath.Join(dir, "missing-skill-stage"), directory: true},
			{component: "telos", target: cli, stage: stageFile(t, filepath.Join(dir, "cli-stage"), "new CLI")},
		})
		if err == nil || strings.Contains(err.Error(), "telos update") {
			t.Fatalf("error = %v; want a failure that updated nothing", err)
		}
		assertFile(t, filepath.Join(skill, "SKILL.md"), "old skill")
		assertFile(t, daemon, "old runtime")
		assertFile(t, cli, "old CLI")
		assertNoPrevious(t, dir)
	})

	t.Run("a later failure names what was updated and keeps the CLI", func(t *testing.T) {
		dir, daemon, skill, cli := setup(t)
		skillStage := filepath.Join(dir, "skill-stage")
		if err := os.MkdirAll(skillStage, 0o755); err != nil {
			t.Fatal(err)
		}
		stageFile(t, filepath.Join(skillStage, "SKILL.md"), "new skill")
		err := installReleaseReplacements([]*releaseReplacement{
			{component: "telosd", target: daemon, stage: stageFile(t, filepath.Join(dir, "runtime-stage"), "new runtime")},
			{component: "telos-cli skill", target: skill, stage: skillStage, directory: true},
			{component: "telos", target: cli, stage: filepath.Join(dir, "missing-cli-stage")},
		})
		if err == nil || !strings.Contains(err.Error(), "updated telosd, telos-cli skill; run `telos update` again to finish") {
			t.Fatalf("error = %v", err)
		}
		assertFile(t, daemon, "new runtime")
		assertFile(t, filepath.Join(skill, "SKILL.md"), "new skill")
		assertFile(t, cli, "old CLI")
		assertNoPrevious(t, dir)
	})
}

func TestInstallationUpdateRejectsUnsafeSkillBeforeReplacingAnything(t *testing.T) {
	for _, entry := range []tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "SKILL.md", Typeflag: tar.TypeSymlink, Linkname: "/tmp/escape"},
		{Name: "references/SKILL.md", Typeflag: tar.TypeReg},
	} {
		t.Run(entry.Name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TELOS_AGENT_SKILLS_DIR", t.TempDir())
			target := filepath.Join(dir, "telos")
			for _, name := range []string{"telos", "telosd"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("original"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var buffer bytes.Buffer
			gz := gzip.NewWriter(&buffer)
			archive := tar.NewWriter(gz)
			if err := archive.WriteHeader(&entry); err != nil {
				t.Fatal(err)
			}
			archive.Close()
			gz.Close()
			server := serveInstallationUpdate(t, "v0.1.5", []byte("replacement"), buffer.Bytes())
			if _, err := updateCLI(target, "v0.1.4", "latest", server.URL, server.Client()); err == nil {
				t.Fatal("accepted unsafe or incomplete skill")
			}
			for _, name := range []string{"telos", "telosd"} {
				if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(data) != "original" {
					t.Fatalf("changed %s: %q, %v", name, data, err)
				}
			}
			assertNoUpdateStage(t, dir)
			assertNoUpdateStage(t, os.Getenv("TELOS_AGENT_SKILLS_DIR"))
		})
	}
}
