package telosd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestCurrentRuntimeIdentityHashesTheRunningExecutable(t *testing.T) {
	identity, err := CurrentRuntimeIdentity(" v0.1.3 ")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Version != "v0.1.3" {
		t.Fatalf("version: got %q", identity.Version)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(identity.TelosdDigest) {
		t.Fatalf("digest: got %q", identity.TelosdDigest)
	}
	path, err := runningExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" && path != "/proc/self/exe" {
		t.Fatalf("Linux executable path: got %q", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256(contents))
	if identity.TelosdDigest != expected {
		t.Fatalf("digest: got %q, want %q", identity.TelosdDigest, expected)
	}
}

func TestRuntimeIdentityHashesTheOpenedExecutableInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telosd")
	if err := os.WriteFile(path, []byte("running bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Rename(path, path+".running"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement bytes"), 0o700); err != nil {
		t.Fatal(err)
	}

	identity, err := runtimeIdentity("v0.1.3", file)
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("running bytes")))
	if identity.TelosdDigest != expected {
		t.Fatalf("digest: got %q, want live inode %q", identity.TelosdDigest, expected)
	}
}

func TestRuntimeIdentityRejectsEmptyVersion(t *testing.T) {
	if _, err := runtimeIdentity("  ", strings.NewReader("telosd")); err == nil {
		t.Fatal("expected empty version error")
	}
}
