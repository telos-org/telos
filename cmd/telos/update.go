package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/spec"
)

const releaseBaseURL = "https://usetelos.ai/releases"

type cliReleaseManifest struct {
	Version   string `json:"version"`
	Platforms []struct {
		OS    string `json:"os"`
		Arch  string `json:"arch"`
		Telos string `json:"telos"`
	} `json:"platforms"`
}

func cmdUpdate(args []string) {
	fs := newCommandFlagSet("update", "telos update [latest|VERSION]")
	parseFlags(fs, args)
	if fs.NArg() > 1 {
		requireArgCount(fs, 1, "at most one release version")
	}
	requested := fs.Arg(0)
	executable, err := os.Executable()
	if err == nil && Version == "dev" {
		err = fmt.Errorf("development builds must be rebuilt from source; install a released CLI to use telos update")
	}
	client := &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing release redirect to non-HTTPS URL")
			}
			if len(via) >= 10 {
				return fmt.Errorf("too many release redirects")
			}
			return nil
		},
	}
	var version string
	if err == nil {
		version, err = updateCLI(executable, Version, requested, releaseBaseURL, client)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if version == Version {
		fmt.Printf("telos is already up to date (%s)\n", version)
		return
	}
	fmt.Printf("updated telos %s -> %s\n", Version, version)
}

func updateCLI(executable, current, requested, baseURL string, client *http.Client) (string, error) {
	version := requested
	if version == "" {
		version = "latest"
	}
	if version != "latest" {
		if !validCLIReleaseVersion(version) {
			return "", fmt.Errorf("invalid release version %q; use latest or a version such as v0.1.5", version)
		}
		version = "v" + strings.TrimPrefix(version, "v")
	}
	target, err := cliUpdateTarget(executable)
	if err != nil {
		return "", err
	}
	var metadata bytes.Buffer
	if err := downloadCLIUpdate(client, baseURL+"/"+version+"/manifest.json", &metadata, 1<<20); err != nil {
		return "", err
	}
	var manifest cliReleaseManifest
	if err := json.Unmarshal(metadata.Bytes(), &manifest); err != nil {
		return "", fmt.Errorf("invalid release manifest: %w", err)
	}
	if !strings.HasPrefix(manifest.Version, "v") || !validCLIReleaseVersion(manifest.Version) {
		return "", fmt.Errorf("invalid version in release manifest: %q", manifest.Version)
	}
	if version != "latest" && manifest.Version != version {
		return "", fmt.Errorf("release manifest version %q does not match requested %q", manifest.Version, version)
	}
	artifact, err := cliReleaseArtifact(manifest, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	if manifest.Version == current {
		return current, nil
	}
	// Pin every subsequent request to the immutable version, even if latest moves.
	immutableURL := baseURL + "/" + manifest.Version
	metadata.Reset()
	if err := downloadCLIUpdate(client, immutableURL+"/SHA256SUMS", &metadata, 1<<20); err != nil {
		return "", err
	}
	expected, err := cliReleaseChecksum(metadata.String(), artifact)
	if err != nil {
		return "", err
	}
	stage, err := os.CreateTemp(filepath.Dir(target), ".telos-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot stage CLI update beside %s (check directory permissions): %w", target, err)
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	hash := sha256.New()
	if err := downloadCLIUpdate(client, immutableURL+"/"+artifact, io.MultiWriter(stage, hash), 256<<20); err != nil {
		return "", err
	}
	if !bytes.Equal(hash.Sum(nil), expected) {
		return "", fmt.Errorf("checksum verification failed for %s; existing CLI is unchanged", artifact)
	}
	if err := stage.Chmod(0o755); err != nil {
		return "", err
	}
	if err := stage.Sync(); err != nil {
		return "", err
	}
	if err := stage.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(stage.Name(), target); err != nil {
		return "", fmt.Errorf("replace CLI %s: %w", target, err)
	}
	return manifest.Version, nil
}

func validCLIReleaseVersion(version string) bool {
	bare := strings.TrimPrefix(version, "v")
	return bare == strings.TrimSpace(bare) && spec.IsSemver(bare)
}

func cliUpdateTarget(executable string) (string, error) {
	target, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve CLI executable: %w", err)
	}
	for _, managed := range []struct{ path, command string }{
		{"/Cellar/", "brew upgrade telos"},
		{"/nix/store/", "your Nix configuration"},
		{"/snap/", "snap refresh telos"},
		{"/opt/local/", "port upgrade telos"},
	} {
		if strings.Contains(filepath.ToSlash(target), managed.path) {
			return "", fmt.Errorf("CLI is package-manager-owned; update it with %s", managed.command)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("CLI executable is not a regular file: %s", target)
	}
	return target, nil
}

func cliReleaseArtifact(manifest cliReleaseManifest, goos, goarch string) (string, error) {
	if (goos != "darwin" && goos != "linux") || (goarch != "amd64" && goarch != "arm64") {
		return "", fmt.Errorf("CLI updates are unsupported on %s/%s", goos, goarch)
	}
	for _, platform := range manifest.Platforms {
		if platform.OS == goos && platform.Arch == goarch {
			if platform.Telos != "telos-"+goos+"-"+goarch {
				return "", fmt.Errorf("invalid CLI artifact in release manifest: %q", platform.Telos)
			}
			return platform.Telos, nil
		}
	}
	return "", fmt.Errorf("release %s has no CLI for %s/%s", manifest.Version, goos, goarch)
}

func cliReleaseChecksum(sums, artifact string) ([]byte, error) {
	var checksum []byte
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != artifact {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size || checksum != nil {
			return nil, fmt.Errorf("invalid or duplicate checksum for %s", artifact)
		}
		checksum = decoded
	}
	if checksum == nil {
		return nil, fmt.Errorf("checksum missing for %s", artifact)
	}
	return checksum, nil
}

func downloadCLIUpdate(client *http.Client, url string, out io.Writer, limit int64) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if n > limit {
		return fmt.Errorf("release download exceeds %d bytes", limit)
	}
	return nil
}
