package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func updateTestManifest(version string) string {
	return fmt.Sprintf(`{"version":%q,"base_url":"https://unused.invalid","platforms":[{"os":%q,"arch":%q,"telos":%q}]}`,
		version, runtime.GOOS, runtime.GOARCH, "telos-"+runtime.GOOS+"-"+runtime.GOARCH)
}

func TestCLIUpdateRejectsDevelopmentBuilds(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected release request", http.StatusInternalServerError)
	}))
	defer srv.Close()
	for _, current := range []string{"dev", "v0.0.0-dev.a7f94f8013ea"} {
		for _, requested := range []string{"latest", "v0.1.5"} {
			t.Run(current+"/"+requested, func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "telos")
				if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
					t.Fatal(err)
				}
				if _, err := updateCLI(target, current, requested, srv.URL, srv.Client()); err == nil || !strings.Contains(err.Error(), "development builds must be rebuilt from source") {
					t.Fatalf("error = %v", err)
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "original" {
					t.Fatalf("original changed: %q, %v", data, err)
				}
				assertNoUpdateStage(t, dir)
			})
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("development build made %d release requests", requests.Load())
	}
}

func TestCLIUpdateReleaseSelection(t *testing.T) {
	for _, requested := range []string{"", "latest", "v0.1.5+master.abc123", "0.1.5+master.abc123"} {
		t.Run(requested, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "telos")
			for _, name := range []string{"telos", "telosd", "config.yaml", "SKILL.md"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("original"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			version := "v0.1.5+master.abc123"
			artifact := "telos-" + runtime.GOOS + "-" + runtime.GOARCH
			payload := []byte("verified replacement")
			manifestVersion := version
			if requested == "" || requested == "latest" {
				manifestVersion = "latest"
			}
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch r.URL.Path {
				case "/" + manifestVersion + "/manifest.json":
					fmt.Fprint(w, updateTestManifest(version))
				case "/" + version + "/SHA256SUMS":
					fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(payload), artifact)
				case "/" + version + "/" + artifact:
					w.Write(payload)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			// An explicit older release is allowed: no semver ordering blocks rollback.
			got, err := updateCLI(target, "v0.2.0+master.def456", requested, srv.URL, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			if got != version {
				t.Fatalf("version = %q", got)
			}
			data, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatalf("replacement = %q, %v", data, err)
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("replacement permissions: %v, %v", info, err)
			}
			for _, name := range []string{"telosd", "config.yaml", "SKILL.md"} {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != "original" {
					t.Fatalf("changed %s", name)
				}
			}
			if requests.Load() != 3 {
				t.Fatalf("requests = %d", requests.Load())
			}
			assertNoUpdateStage(t, dir)
		})
	}
}

func TestCLIUpdateNoop(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "telos")
	if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest/manifest.json" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		fmt.Fprint(w, updateTestManifest("v0.1.5"))
	}))
	defer srv.Close()
	got, err := updateCLI(target, "v0.1.5", "latest", srv.URL, srv.Client())
	if err != nil || got != "v0.1.5" {
		t.Fatalf("update = %q, %v", got, err)
	}
	after, _ := os.Stat(target)
	if !os.SameFile(before, after) {
		t.Fatal("no-op replaced the executable")
	}
	assertNoUpdateStage(t, dir)
}

func TestCLIUpdateFailuresKeepOriginal(t *testing.T) {
	for _, failure := range []string{"manifest-http", "manifest-json", "manifest-version", "wrong-version", "missing-platform", "unsafe-artifact", "checksum-http", "checksum-missing", "checksum-invalid", "checksum-duplicate", "checksum-mismatch", "binary-http", "truncated-binary"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "telos")
			if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
				t.Fatal(err)
			}
			payload := []byte("new binary")
			artifact := "telos-" + runtime.GOOS + "-" + runtime.GOARCH
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch filepath.Base(r.URL.Path) {
				case "manifest.json":
					switch failure {
					case "manifest-http":
						http.Error(w, "not found", 404)
					case "manifest-json":
						fmt.Fprint(w, "not json")
					case "manifest-version":
						fmt.Fprint(w, updateTestManifest("../../unsafe"))
					case "wrong-version":
						fmt.Fprint(w, updateTestManifest("v0.9.0"))
					case "missing-platform":
						fmt.Fprint(w, `{"version":"v0.1.5","platforms":[]}`)
					case "unsafe-artifact":
						fmt.Fprint(w, strings.ReplaceAll(updateTestManifest("v0.1.5"), artifact, "../telosd"))
					default:
						fmt.Fprint(w, updateTestManifest("v0.1.5"))
					}
				case "SHA256SUMS":
					line := fmt.Sprintf("%x  %s\n", sha256.Sum256(payload), artifact)
					switch failure {
					case "checksum-http":
						http.Error(w, "unavailable", 503)
					case "checksum-missing":
						fmt.Fprint(w, "")
					case "checksum-invalid":
						fmt.Fprintf(w, "not-a-hash  %s\n", artifact)
					case "checksum-duplicate":
						fmt.Fprint(w, line+line)
					case "checksum-mismatch":
						fmt.Fprintf(w, "%x  %s\n", sha256.Sum256([]byte("other")), artifact)
					default:
						fmt.Fprint(w, line)
					}
				case artifact:
					if failure == "binary-http" {
						http.Error(w, "unavailable", 503)
						return
					}
					if failure == "truncated-binary" {
						w.Header().Set("Content-Length", "1000")
					}
					w.Write(payload)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			if _, err := updateCLI(target, "v0.1.4", "v0.1.5", srv.URL, srv.Client()); err == nil {
				t.Fatal("expected update failure")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "original" {
				t.Fatalf("original changed: %q, %v", data, err)
			}
			assertNoUpdateStage(t, dir)
		})
	}
}

func TestCLIUpdatePreservesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-telos")
	link := filepath.Join(dir, "telos")
	if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := cliUpdateTarget(link)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("target = %q", resolved)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("changed symlink")
	}
}

func TestCLIUpdateRejectsManagedTargets(t *testing.T) {
	for _, path := range []string{"opt/homebrew/Cellar/telos/0.1.5/bin/telos", "nix/store/hash-telos/bin/telos", "snap/telos/1/bin/telos", "opt/local/bin/telos"} {
		t.Run(path, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), path)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("managed"), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := cliUpdateTarget(target); err == nil || !strings.Contains(err.Error(), "package-manager") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCLIReleaseVersions(t *testing.T) {
	for _, valid := range []string{"v0.1.5", "0.1.5", "v0.1.5+master.abc123", "v1.2.3-rc.1"} {
		if !validCLIReleaseVersion(valid) {
			t.Errorf("rejected %q", valid)
		}
	}
	for _, invalid := range []string{"", "dev", "latest", "v1", "v01.2.3", "../../etc", "v1.2.3/foo", "v1.2.3?foo", "v1.2.3#foo", " v1.2.3", "v 1.2.3", "v1.2.3\n"} {
		if validCLIReleaseVersion(invalid) {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestCLIUpdateDownloadBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "too large") }))
	defer srv.Close()
	var out bytes.Buffer
	if err := downloadCLIUpdate(srv.Client(), srv.URL, &out, 3); err == nil {
		t.Fatal("accepted oversized response")
	}
	if out.Len() != 4 {
		t.Fatalf("read %d bytes", out.Len())
	}
}

func TestCLIUpdateRunningExecutable(t *testing.T) {
	if endpoint := os.Getenv("TELOS_TEST_SELF_UPDATE_URL"); endpoint != "" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := updateCLI(executable, "v0.1.4", "latest", endpoint, &http.Client{Timeout: time.Second * 10}); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "telos")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	dest, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dest, source); err != nil {
		t.Fatal(err)
	}
	if err := dest.Close(); err != nil {
		t.Fatal(err)
	}
	payload := []byte("replacement CLI")
	artifact := "telos-" + runtime.GOOS + "-" + runtime.GOARCH
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "manifest.json":
			fmt.Fprint(w, updateTestManifest("v0.1.5"))
		case "SHA256SUMS":
			fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(payload), artifact)
		case artifact:
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cmd := exec.Command(target, "-test.run=^TestCLIUpdateRunningExecutable$")
	cmd.Env = append(os.Environ(), "TELOS_TEST_SELF_UPDATE_URL="+srv.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running update: %v\n%s", err, out)
	}
	data, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("replacement = %q, %v", data, err)
	}
	assertNoUpdateStage(t, dir)
}

func assertNoUpdateStage(t *testing.T, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, ".telos-update-*"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("staging files left: %v, %v", paths, err)
	}
}
