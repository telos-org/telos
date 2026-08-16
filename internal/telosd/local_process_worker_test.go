package telosd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestNewSessionSubstrateDefaultsToLocalProcess(t *testing.T) {
	cfg, err := NormalizeConfig(Config{
		Mode: ModeCloud,
		Auth: AuthConfig{Token: "operator-token"},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	substrate, err := newSessionSubstrate(cfg)
	if err != nil {
		t.Fatalf("newSessionSubstrate: %v", err)
	}
	if _, ok := substrate.(localProcessSubstrate); !ok {
		t.Fatalf("substrate: got %T", substrate)
	}
}

func TestLocalProcessSubstrateStartsCloudWorker(t *testing.T) {
	dir := t.TempDir()
	fakeTelosd := filepath.Join(dir, "telosd")
	if err := os.WriteFile(fakeTelosd, []byte(`#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--session-dir" ]; then
    shift
    session_dir="$1"
  fi
  shift
done
{
  echo "TELOS_RUNTIME=$TELOS_RUNTIME"
  echo "TELOS_SESSION_ID=$TELOS_SESSION_ID"
  echo "TELOS_API_TOKEN=$TELOS_API_TOKEN"
  echo "TELOS_WAKE_REASON=$TELOS_WAKE_REASON"
  echo "TELOS_CHECKPOINT_ROOT=$TELOS_CHECKPOINT_ROOT"
  echo "TELOS_CHECKPOINT_SESSION_ID=$TELOS_CHECKPOINT_SESSION_ID"
} > "$session_dir/worker.env"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOSD_PATH", fakeTelosd)

	store := sessionapi.NewFileStore(filepath.Join(dir, "sessions"), sessionapi.RuntimeCloud)
	markdown := "---\nversion: 0.1.0\nname: auth\nplatform: cloud\n---\n# Auth\n"
	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	substrate := localProcessSubstrate{
		checkpointRoot:      dir,
		checkpointSessionID: "sess_deployment_123",
	}
	if err := substrate.Apply(session, "controller_started"); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	envPath := filepath.Join(*session.SessionDir, "worker.env")
	var data []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(envPath)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read worker env: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"TELOS_RUNTIME=cloud",
		"TELOS_SESSION_ID=" + session.SessionID,
		"TELOS_WAKE_REASON=controller_started",
		"TELOS_CHECKPOINT_ROOT=" + dir,
		"TELOS_CHECKPOINT_SESSION_ID=sess_deployment_123",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("worker env missing %q:\n%s", want, text)
		}
	}
}

func TestNewSessionSubstrateScopesCheckpointAdmissionToClaimedCloudRuntime(t *testing.T) {
	t.Setenv("TELOS_SESSION_ID", "sess_deployment_123")
	root := t.TempDir()
	substrate, err := newSessionSubstrate(Config{Mode: ModeCloud, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	local, ok := substrate.(localProcessSubstrate)
	if !ok {
		t.Fatalf("substrate: got %T", substrate)
	}
	if local.checkpointRoot != root || local.checkpointSessionID != "sess_deployment_123" {
		t.Fatalf("checkpoint scope: root=%q session=%q", local.checkpointRoot, local.checkpointSessionID)
	}
}
