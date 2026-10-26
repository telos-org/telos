package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWindowsInstallerAndRunningCLISelfUpdate(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{}
	var corruptChecksum atomic.Bool
	var corruptSkill atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest/manifest.json" {
			fmt.Fprint(w, updateTestManifest("v0.1.5"))
			return
		}
		if data, ok := payloads[r.URL.Path]; ok {
			if corruptChecksum.Load() && strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
				data = append([]byte(strings.Repeat("0", 64)), data[64:]...)
			}
			if corruptSkill.Load() && strings.HasSuffix(r.URL.Path, "/telos-cli-skill.tar.gz") {
				data = []byte("corrupt skill")
			}
			_, _ = w.Write(data)
			return
		}
		t.Errorf("unexpected release request: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	directory := t.TempDir()
	for _, version := range []string{"v0.1.4", "v0.1.5"} {
		binaryPath := filepath.Join(directory, version+".exe")
		build := exec.Command("go", "build", "-ldflags", "-X main.Version="+version+" -X main.releaseBaseURL="+server.URL, "-o", binaryPath, "./cmd/telos")
		build.Dir = repository
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", version, err, output)
		}
		binary, err := os.ReadFile(binaryPath)
		if err != nil {
			t.Fatal(err)
		}
		skill := updateTestSkill(t)
		if version == "v0.1.4" {
			skill = updateTestSkillFiles(t, map[string]string{"SKILL.md": "old bundled skill"})
		}
		payloads["/"+version+"/telos-windows-amd64.exe"] = binary
		payloads["/"+version+"/telos-cli-skill.tar.gz"] = skill
		payloads["/"+version+"/SHA256SUMS"] = []byte(updateTestSums(binary, skill))
		payloads["/"+version+"/manifest.json"] = []byte(updateTestManifest(version))
	}
	installer, err := os.ReadFile(filepath.Join(repository, "scripts", "install-release.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	installer = bytes.ReplaceAll(installer, []byte("@TELOS_VERSION@"), []byte("v0.1.4"))
	installerPath := filepath.Join(directory, "install.ps1")
	if err := os.WriteFile(installerPath, installer, 0o644); err != nil {
		t.Fatal(err)
	}
	installDirectory := filepath.Join(directory, "bin with spaces")
	skillsDirectory := filepath.Join(directory, "skills with spaces")
	t.Setenv("TELOS_INSTALL_DIR", installDirectory+string(os.PathSeparator))
	t.Setenv("TELOS_AGENT_SKILLS_DIR", skillsDirectory+string(os.PathSeparator))
	t.Setenv("TELOS_RELEASE_BASE_URL", server.URL)
	t.Setenv("TELOS_CONFIG", filepath.Join(directory, "config.yaml"))
	installationShell := "powershell.exe"
	install := func() (string, error) {
		command := exec.Command(installationShell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", installerPath)
		output, err := command.CombinedOutput()
		return string(output), err
	}
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		installationShell = shell
		if output, err := install(); err != nil {
			t.Fatalf("PowerShell install: %v\n%s", err, output)
		}
	}
	target := filepath.Join(installDirectory, "telos.exe")
	command := func(args ...string) (string, error) {
		output, err := exec.Command(target, args...).CombinedOutput()
		return string(output), err
	}
	if output, err := command("--version"); err != nil || strings.TrimSpace(output) != "telos v0.1.4" {
		t.Fatalf("installed CLI: %v %s", err, output)
	}
	t.Setenv("TELOS_SESSION_ID", "hosted-session")
	t.Setenv("TELOS_API_TOKEN", "hosted-token")
	if output, err := command("--help"); err != nil || strings.Contains(output, "run SPEC.md") {
		t.Fatalf("Windows help: %v %s", err, output)
	}
	if output, err := command("run", "SPEC.md"); err == nil || !strings.Contains(output, "not supported on Windows") {
		t.Fatalf("Windows run: %v %s", err, output)
	}
	corruptChecksum.Store(true)
	if output, err := install(); err == nil || !strings.Contains(output, "Checksum verification failed") {
		t.Fatalf("installer accepted a corrupt release: %v %s", err, output)
	}
	corruptChecksum.Store(false)
	corruptSkill.Store(true)
	if output, err := install(); err == nil || !strings.Contains(strings.ToLower(output), "checksum verification failed") {
		t.Fatalf("installer accepted a corrupt skill: %v %s", err, output)
	}
	corruptSkill.Store(false)
	if output, err := command("--version"); err != nil || strings.TrimSpace(output) != "telos v0.1.4" {
		t.Fatalf("failed install changed CLI: %v %s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(skillsDirectory, "telos-cli", "SKILL.md")); err != nil || string(data) != "old bundled skill" {
		t.Fatalf("failed install changed skill: %q %v", data, err)
	}
	lock, err := lockInstallation(filepath.Join(installDirectory, ".telos-install.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if output, err := command("update"); err == nil || !strings.Contains(output, "cannot lock installation") {
		lock.Close()
		t.Fatalf("update ignored lock: %v %s", err, output)
	}
	if output, err := install(); err == nil {
		lock.Close()
		t.Fatalf("installer ignored lock: %s", output)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// The updater must remember the installer-selected skill directory.
	t.Setenv("TELOS_AGENT_SKILLS_DIR", "")
	if output, err := command("update"); err != nil || !strings.Contains(output, "updated Telos installation to v0.1.5") {
		t.Fatalf("running self-update: %v\n%s", err, output)
	}
	if output, err := command("--version"); err != nil || strings.TrimSpace(output) != "telos v0.1.5" {
		t.Fatalf("updated CLI: %v %s", err, output)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatalf("next invocation did not clean old binary: %v", err)
	}
	if output, err := command("update"); err != nil || !strings.Contains(output, "already up to date") {
		t.Fatalf("repeated update: %v %s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(skillsDirectory, "telos-cli", "SKILL.md")); err != nil || string(data) != "released SKILL.md" {
		t.Fatalf("bundled skill: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(installDirectory, "telosd.exe")); !os.IsNotExist(err) {
		t.Fatalf("installed Windows runtime: %v", err)
	}
	assertNoUpdateStage(t, installDirectory)
	entries, err := os.ReadDir(installDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".telos-install.") && entry.Name() != ".telos-install.lock" {
			t.Fatalf("installer left stage %s", entry.Name())
		}
	}
}
