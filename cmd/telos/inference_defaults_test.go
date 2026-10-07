package main

import (
	"flag"
	"path/filepath"
	"testing"

	"github.com/telos-org/telos/internal/sessionapi"
)

func TestNestedInferenceDefaultsFollowConfirmedParent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TELOS_SESSION_DIR", root)
	t.Setenv("TELOS_SESSION_ID", "parent")
	t.Setenv("TELOS_MODEL", "provider/old")
	t.Setenv("TELOS_INHERITED_MODEL", "provider/old")
	t.Setenv("TELOS_THINKING", "medium")
	t.Setenv("TELOS_INHERITED_THINKING", "medium")
	// A child that inherited these settings need not have its own update record.
	m := &sessionapi.Manifest{SessionID: "parent", SessionKind: sessionapi.KindController, Config: sessionapi.SessionConfig{Model: "provider/new", Thinking: "max"}}
	if err := sessionapi.WriteManifest(filepath.Join(root, "parent", "session.json"), m); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	model := fs.String("model", "", "")
	level := fs.String("thinking", "", "")
	if got := modelOption(fs, *model); got != "provider/new" {
		t.Fatalf("inherited model: %q", got)
	}
	if got, err := thinkingOption(fs, *level); err != nil || got != "max" {
		t.Fatalf("inherited native thinking: %q %v", got, err)
	}
	t.Setenv("TELOS_MODEL", "provider/manual")
	t.Setenv("TELOS_THINKING", "low")
	if got := modelOption(fs, *model); got != "provider/manual" {
		t.Fatalf("explicit environment: %q", got)
	}
	if got, err := thinkingOption(fs, *level); err != nil || got != "low" {
		t.Fatalf("explicit environment: %q %v", got, err)
	}
	if err := fs.Parse([]string{"--model", "provider/flag", "--thinking", "high"}); err != nil {
		t.Fatal(err)
	}
	if got := modelOption(fs, *model); got != "provider/flag" {
		t.Fatalf("explicit flag: %q", got)
	}
	if got, err := thinkingOption(fs, *level); err != nil || got != "high" {
		t.Fatalf("explicit flag: %q %v", got, err)
	}
}
