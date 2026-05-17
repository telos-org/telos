// Package local implements the local Telos runtime: the telosd daemon, the
// detached worker launcher, and the filesystem layout shared by both.
package local

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RuntimeRoot returns the root directory for ephemeral telosd runtime files
// (sockets, pid files, locks). It is per-user and lives under the temp dir.
func RuntimeRoot() string {
	if v := strings.TrimSpace(os.Getenv("TELOS_RUNTIME_ROOT")); v != "" {
		return absOr(v)
	}
	return absOr(filepath.Join(os.TempDir(), "telos", strconv.Itoa(os.Getuid())))
}

// WorkspaceID returns a stable short identifier for a workspace root, used to
// give each workspace its own telosd socket.
func WorkspaceID(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])[:16]
}

// DaemonPaths holds the per-workspace runtime file paths for telosd.
type DaemonPaths struct {
	RunDir string
	Socket string
	PID    string
	Log    string
	Lock   string
}

// DaemonPathsFor returns the telosd runtime paths for a workspace root.
func DaemonPathsFor(workspaceRoot string) DaemonPaths {
	id := WorkspaceID(absOr(workspaceRoot))
	runDir := filepath.Join(RuntimeRoot(), "run")
	return DaemonPaths{
		RunDir: runDir,
		Socket: filepath.Join(runDir, id+".sock"),
		PID:    filepath.Join(runDir, id+".json"),
		Log:    filepath.Join(runDir, id+".log"),
		Lock:   filepath.Join(runDir, id+".lock"),
	}
}

func absOr(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
