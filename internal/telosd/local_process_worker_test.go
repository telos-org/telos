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
} > "$session_dir/worker.env"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELOSD_PATH", fakeTelosd)

	store := sessionapi.NewFileStore(filepath.Join(dir, "sessions"), sessionapi.RuntimeCloud)
	markdown := "---\nversion: v0\nname: auth\nplatform: cloud\n---\n# Auth\n"
	session, err := store.Create(sessionapi.SessionCreateRequest{SpecMarkdown: &markdown})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	substrate := newLocalProcessSubstrate()
	if err := substrate.Apply(session, "controller_started"); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	envPath := filepath.Join(*session.SessionDir, "worker.env")
	var data []byte
	for i := 0; i < 50; i++ {
		data, err = os.ReadFile(envPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read worker env: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"TELOS_RUNTIME=cloud",
		"TELOS_SESSION_ID=" + session.SessionID,
		"TELOS_WAKE_REASON=controller_started",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("worker env missing %q:\n%s", want, text)
		}
	}
}
