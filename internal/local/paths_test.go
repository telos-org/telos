package local

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceIDStable(t *testing.T) {
	a := WorkspaceID("/home/user/project")
	b := WorkspaceID("/home/user/project")
	if a != b {
		t.Fatalf("WorkspaceID not stable: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("WorkspaceID: want 16 hex chars, got %d", len(a))
	}
	if WorkspaceID("/other/project") == a {
		t.Fatal("distinct workspace roots collided")
	}
}

func TestRuntimeRootHonorsEnv(t *testing.T) {
	t.Setenv("TELOS_RUNTIME_ROOT", "/tmp/custom-runtime")
	if got := RuntimeRoot(); got != "/tmp/custom-runtime" {
		t.Fatalf("RuntimeRoot: got %q", got)
	}
}

func TestDaemonPathsFor(t *testing.T) {
	t.Setenv("TELOS_RUNTIME_ROOT", "/tmp/telos-rt")
	paths := DaemonPathsFor("/home/user/project")

	if paths.RunDir != "/tmp/telos-rt/run" {
		t.Fatalf("RunDir: got %q", paths.RunDir)
	}
	id := WorkspaceID(absOr("/home/user/project"))
	if !strings.HasSuffix(paths.Socket, id+".sock") {
		t.Fatalf("Socket: got %q", paths.Socket)
	}
	for label, p := range map[string]string{
		"socket": paths.Socket,
		"pid":    paths.PID,
		"log":    paths.Log,
		"lock":   paths.Lock,
	} {
		if filepath.Dir(p) != paths.RunDir {
			t.Fatalf("%s path %q not under RunDir %q", label, p, paths.RunDir)
		}
	}
}
