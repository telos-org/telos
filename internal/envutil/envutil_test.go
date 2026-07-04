package envutil

import (
	"testing"
	"time"
)

func TestBoolGrammar(t *testing.T) {
	for _, raw := range []string{"1", "true", "TRUE", "yes", "On", " on "} {
		if !Bool(raw) {
			t.Fatalf("Bool(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"", "0", "false", "no", "off", "maybe"} {
		if Bool(raw) {
			t.Fatalf("Bool(%q) = true, want false", raw)
		}
	}
}

func TestInt(t *testing.T) {
	if got := Int("42", 7); got != 42 {
		t.Fatalf("Int: got %d", got)
	}
	if got := Int("bad", 7); got != 7 {
		t.Fatalf("Int fallback: got %d", got)
	}
}

func TestDurationMS(t *testing.T) {
	if got := DurationMS("250", time.Second); got != 250*time.Millisecond {
		t.Fatalf("DurationMS: got %s", got)
	}
	if got := DurationMS("-1", time.Second); got != time.Second {
		t.Fatalf("DurationMS fallback: got %s", got)
	}
}
