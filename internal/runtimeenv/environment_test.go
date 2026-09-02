package runtimeenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestStoreUnseededStatusDoesNotExposeEnvironment(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))

	status, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || status.Seeded || status.Generation != 0 || status.Count != 0 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if got, want := status.EnvironmentSHA256, "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"; got != want {
		t.Fatalf("empty environment digest: got %q want %q", got, want)
	}
}

func TestEnvironmentSHA256UsesLengthPrefixedUTF8Entries(t *testing.T) {
	environment := map[string]string{
		"Z": "<&>",
		"é": "雪",
	}
	if got, want := EnvironmentSHA256(environment), "sha256:0ed8d3aae9c9a66ac100bf500eaf4eb80cfc274429b456fe3c1b65613e04b490"; got != want {
		t.Fatalf("environment digest: got %q want %q", got, want)
	}
}

func TestStoreAllowsSeededGenerationZeroForEmptyEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)

	status, err := store.Put(0, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Seeded || status.Generation != 0 || status.Count != 0 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if _, err := store.Put(0, map[string]string{"TOKEN": "opaque"}); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("non-empty generation zero: got %v want ErrInvalidEnvironment", err)
	}
	if _, err := store.Put(1, map[string]string{"TOKEN": "opaque"}); err != nil {
		t.Fatalf("attach after generation zero: %v", err)
	}
	state, seeded, err := Read(path)
	if err != nil || !seeded {
		t.Fatalf("read attached state: seeded=%v err=%v", seeded, err)
	}
	if got := testEnvironmentMap(Apply([]string{"TOKEN=stale"}, state))["TOKEN"]; got != "opaque" {
		t.Fatalf("attached token: got %q want opaque", got)
	}
	if _, err := store.Put(2, map[string]string{}); err != nil {
		t.Fatalf("detach after generation zero: %v", err)
	}
	state, _, err = Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := testEnvironmentMap(Apply([]string{"TOKEN=stale"}, state))["TOKEN"]; ok {
		t.Fatal("detached token remained after generation-zero seed")
	}
}

func TestInitializedStoreFailsClosedWhenStateDisappears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	if _, err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("get missing configured state: got %v want ErrInvalidState", err)
	}
	if _, err := store.Put(1, map[string]string{"TOKEN": "opaque"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("put missing configured state: got %v want ErrInvalidState", err)
	}
}

func TestStoreGenerationAndIdempotency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	initial := map[string]string{"MODAL_TOKEN_ID": "opaque-one"}

	status, err := store.Put(4, initial)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Seeded || status.Generation != 4 || status.Count != 1 {
		t.Fatalf("unexpected initial status: %#v", status)
	}
	if _, err := store.Put(3, initial); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale generation: got %v want ErrStaleGeneration", err)
	}
	if _, err := store.Put(4, initial); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	if _, err := store.Put(4, map[string]string{"MODAL_TOKEN_ID": "different"}); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("conflicting generation: got %v want ErrGenerationConflict", err)
	}

	next := map[string]string{"MODAL_TOKEN_SECRET": "opaque-two"}
	status, err = store.Put(6, next)
	if err != nil {
		t.Fatal(err)
	}
	if status.Generation != 6 || status.Count != 1 {
		t.Fatalf("unexpected next status: %#v", status)
	}
	state, seeded, err := Read(path)
	if err != nil || !seeded {
		t.Fatalf("read state: seeded=%v err=%v", seeded, err)
	}
	if got, want := state.ManagedNames, []string{"MODAL_TOKEN_ID", "MODAL_TOKEN_SECRET"}; !slices.Equal(got, want) {
		t.Fatalf("managed names: got %v want %v", got, want)
	}
	if got, want := state.EnvironmentSHA256, EnvironmentSHA256(next); got != want {
		t.Fatalf("digest: got %q want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state permissions: got %o want 600", got)
	}
	reopenedStatus, err := NewStore(path).Get()
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	if reopenedStatus != status {
		t.Fatalf("reopened status: got %#v want %#v", reopenedStatus, status)
	}
}

func TestSameGenerationDistinguishesDifferentEmptyValuedKeys(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if _, err := store.Put(1, map[string]string{"TOKEN_A": ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(1, map[string]string{"TOKEN_B": ""}); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("same generation with a different key: got %v want ErrGenerationConflict", err)
	}
}

func TestStoreRejectsReservedAndInvalidEnvironment(t *testing.T) {
	tooMany := make(map[string]string, maxEnvironmentEntries+1)
	for index := range maxEnvironmentEntries + 1 {
		tooMany[fmt.Sprintf("TOKEN_%d", index)] = "opaque"
	}
	tests := []map[string]string{
		nil,
		{"bad-name": "opaque"},
		{"telos_api_token": "opaque"},
		{"Path": "opaque"},
		{"TOKEN": "contains\x00nul"},
		{"TOKEN": strings.Repeat("x", maxEnvironmentValue+1)},
		tooMany,
	}
	for index, environment := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "state.json"))
			if _, err := store.Put(1, environment); !errors.Is(err, ErrInvalidEnvironment) {
				t.Fatalf("invalid environment: got %v want ErrInvalidEnvironment", err)
			}
		})
	}
}

func TestApplyMasksDetachedInheritedNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	if _, err := store.Put(1, map[string]string{
		"MODAL_TOKEN_ID":     "opaque-one",
		"MODAL_TOKEN_SECRET": "opaque-two",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(2, map[string]string{"NEW_TOKEN": "opaque-three"}); err != nil {
		t.Fatal(err)
	}
	state, _, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	got := testEnvironmentMap(Apply([]string{
		"MODAL_TOKEN_ID=opaque-one",
		"MODAL_TOKEN_SECRET=opaque-two",
		"UNCHANGED=keep",
	}, state))
	if _, ok := got["MODAL_TOKEN_ID"]; ok {
		t.Fatal("detached MODAL_TOKEN_ID remained in the child environment")
	}
	if _, ok := got["MODAL_TOKEN_SECRET"]; ok {
		t.Fatal("detached MODAL_TOKEN_SECRET remained in the child environment")
	}
	if got["NEW_TOKEN"] != "opaque-three" || got["UNCHANGED"] != "keep" {
		t.Fatalf("unexpected applied environment: %v", got)
	}

	if _, err := store.Put(3, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	state, _, err = Read(path)
	if err != nil {
		t.Fatal(err)
	}
	got = testEnvironmentMap(Apply([]string{
		"MODAL_TOKEN_ID=opaque-one",
		"MODAL_TOKEN_SECRET=opaque-two",
		"UNCHANGED=keep",
	}, state))
	if _, ok := got["NEW_TOKEN"]; ok {
		t.Fatal("detached dynamically added token remained in the child environment")
	}
	if got["UNCHANGED"] != "keep" {
		t.Fatalf("unmanaged environment changed: %v", got)
	}
}

func TestReadRejectsCorruptOrUnsafeState(t *testing.T) {
	t.Run("invalid digest", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		data := []byte(`{"kind":"telos.runtime-credential-environment.v1","generation":1,"managed_names":["TOKEN"],"environment":{"TOKEN":"opaque"},"environment_sha256":"sha256:wrong"}`)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Read(path); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("invalid digest: got %v want ErrInvalidState", err)
		}
	})

	t.Run("unsafe permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Read(path); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("unsafe permissions: got %v want ErrInvalidState", err)
		}
	})
}

func TestAtomicStateIsAlwaysReadableDuringUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	if _, err := store.Put(0, map[string]string{}); err != nil {
		t.Fatal(err)
	}

	const updates = 100
	var readers sync.WaitGroup
	errCh := make(chan error, 4)
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range updates {
				state, seeded, err := Read(path)
				if err != nil {
					errCh <- err
					return
				}
				if !seeded || state.EnvironmentSHA256 != EnvironmentSHA256(state.Environment) {
					errCh <- errors.New("reader observed a partial state")
					return
				}
			}
		}()
	}
	for generation := uint64(1); generation <= updates; generation++ {
		if _, err := store.Put(generation, map[string]string{
			"TOKEN": fmt.Sprintf("opaque-%d", generation),
		}); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func testEnvironmentMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	return values
}
