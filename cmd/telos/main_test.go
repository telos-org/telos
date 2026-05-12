package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestReorderInterspersedFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")
	fs.String("workspace", "", "")
	fs.Int("max-rounds", 0, "")

	got := reorderInterspersedFlags(fs, []string{
		"SPEC.md",
		"--json",
		"--workspace",
		"/tmp/ws",
		"--max-rounds=3",
	})
	want := []string{
		"--json",
		"--workspace",
		"/tmp/ws",
		"--max-rounds=3",
		"SPEC.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestReorderInterspersedFlagsDashDash(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("json", false, "")

	got := reorderInterspersedFlags(fs, []string{"--json", "--", "-literal"})
	want := []string{"--json", "-literal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
