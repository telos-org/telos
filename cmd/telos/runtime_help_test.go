package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageShowsRunForAvailableExecution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mode   os.FileMode
		hosted bool
		show   bool
	}{
		{name: "missing runtime"},
		{name: "non-executable runtime", mode: 0o644},
		{name: "local runtime", mode: 0o755, show: true},
		{name: "hosted nested execution", hosted: true, show: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := filepath.Join(t.TempDir(), "telosd")
			if test.mode != 0 {
				if err := os.WriteFile(runtime, []byte("#!/bin/sh\n"), test.mode); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TELOSD_PATH", runtime)
			t.Setenv("TELOS_RUNTIME", "")
			t.Setenv("TELOS_API_TOKEN", "")
			t.Setenv("TELOS_SESSION_ID", "")
			if test.hosted {
				t.Setenv("TELOS_API_TOKEN", "test-session-token")
				t.Setenv("TELOS_SESSION_ID", "test-session")
			}
			var out bytes.Buffer
			usage(&out)
			if strings.Contains(out.String(), "run SPEC.md") != test.show {
				t.Fatalf("unexpected run visibility:\n%s", out.String())
			}
			for _, command := range []string{"apply SPEC.md", "list", "describe SESSION", "logs SESSION", "update [VERSION]"} {
				if !strings.Contains(out.String(), command) {
					t.Fatalf("Cloud client help lost %s", command)
				}
			}
		})
	}
}
