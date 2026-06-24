package main

import (
	"flag"
	"os"
	"testing"
)

func newAutocompactFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Int("autocompact-context-window", 0, "")
	fs.Float64("autocompact-trigger-ratio", 0, "")
	fs.Int("autocompact-keep-recent-tokens", 0, "")
	return fs
}

func TestExportAutocompactEnvSetsOnlySetFlags(t *testing.T) {
	os.Unsetenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW")
	os.Unsetenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO")
	t.Setenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS", "99") // pre-existing env, must survive

	fs := newAutocompactFlagSet()
	if err := fs.Parse([]string{"--autocompact-context-window=64000", "--autocompact-trigger-ratio=0.5"}); err != nil {
		t.Fatal(err)
	}

	if err := exportAutocompactEnv(fs, autocompactFlags{ContextWindow: 64000, TriggerRatio: 0.5}); err != nil {
		t.Fatal(err)
	}

	if got := os.Getenv("TELOS_AUTOCOMPACT_CONTEXT_WINDOW"); got != "64000" {
		t.Fatalf("context window env: got %q", got)
	}
	if got := os.Getenv("TELOS_AUTOCOMPACT_TRIGGER_RATIO"); got != "0.5" {
		t.Fatalf("trigger ratio env: got %q", got)
	}
	if got := os.Getenv("TELOS_AUTOCOMPACT_KEEP_RECENT_TOKENS"); got != "99" {
		t.Fatalf("unset flag must not clobber existing env: got %q", got)
	}
}

func TestExportAutocompactEnvValidates(t *testing.T) {
	cases := []struct {
		name string
		args []string
		vals autocompactFlags
	}{
		{"ratio too high", []string{"--autocompact-trigger-ratio=1.5"}, autocompactFlags{TriggerRatio: 1.5}},
		{"ratio zero set", []string{"--autocompact-trigger-ratio=0"}, autocompactFlags{TriggerRatio: 0}},
		{"negative window", []string{"--autocompact-context-window=-1"}, autocompactFlags{ContextWindow: -1}},
		{"non-positive keep", []string{"--autocompact-keep-recent-tokens=0"}, autocompactFlags{KeepRecentTokens: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newAutocompactFlagSet()
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if err := exportAutocompactEnv(fs, tc.vals); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
