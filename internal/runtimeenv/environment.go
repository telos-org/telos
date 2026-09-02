// Package runtimeenv stores the non-secret environment values used by the
// current runtime credential policy.
package runtimeenv

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
)

const (
	Kind                    = "telos.runtime-credential-environment.v1"
	PathEnvironmentVariable = "TELOS_RUNTIME_CREDENTIAL_ENV_FILE"

	maxEnvironmentEntries = 100
	maxManagedNames       = 1024
	maxEnvironmentValue   = 4096
	maxStateBytes         = 2 << 20
)

var (
	ErrInvalidEnvironment = errors.New("invalid runtime credential environment")
	ErrInvalidState       = errors.New("invalid runtime credential environment state")
	ErrStaleGeneration    = errors.New("runtime credential environment generation is stale")
	ErrGenerationConflict = errors.New("runtime credential environment generation conflicts")
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)

var reservedEnvironmentNames = map[string]struct{}{
	"ALL_PROXY":           {},
	"CURL_CA_BUNDLE":      {},
	"DENO_CERT":           {},
	"GIT_SSL_CAINFO":      {},
	"HOME":                {},
	"HTTPS_PROXY":         {},
	"HTTP_PROXY":          {},
	"NODE_EXTRA_CA_CERTS": {},
	"NODE_USE_ENV_PROXY":  {},
	"NO_PROXY":            {},
	"OPENCLAW_PROXY_URL":  {},
	"PATH":                {},
	"REQUESTS_CA_BUNDLE":  {},
	"SSL_CERT_DIR":        {},
	"SSL_CERT_FILE":       {},
}

// State is the complete durable credential environment snapshot. Environment
// contains only opaque workload values, never the backing credential secrets.
type State struct {
	Kind              string            `json:"kind"`
	Generation        uint64            `json:"generation"`
	ManagedNames      []string          `json:"managed_names"`
	Environment       map[string]string `json:"environment"`
	EnvironmentSHA256 string            `json:"environment_sha256"`
}

// Status is safe for the control plane to inspect. It deliberately excludes
// both current and historical environment values and names.
type Status struct {
	Supported         bool   `json:"supported"`
	Seeded            bool   `json:"seeded"`
	Generation        uint64 `json:"generation"`
	Count             int    `json:"count"`
	EnvironmentSHA256 string `json:"environment_sha256"`
}

// Store serializes control-plane updates to one atomic state file. Readers in
// worker subprocesses do not share this lock and rely on atomic rename.
type Store struct {
	path         string
	requireState bool
	mu           sync.Mutex
}

// Path returns the credential environment state path for a telosd state root.
func Path(root string) string {
	return filepath.Join(root, "runtime", "credential-environment.json")
}

// NewStore creates a state store. It does not create or replace state.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Initialize opts this runtime into dynamic credential environments. It
// creates a seeded generation-zero snapshot before workers start and makes a
// subsequently missing state file an error for this server process.
func (s *Store) Initialize() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := normalizeStatePermissions(s.path); err != nil {
		return Status{}, err
	}
	state, seeded, err := Read(s.path)
	if err != nil {
		return Status{}, err
	}
	if !seeded {
		state = State{
			Kind:              Kind,
			Generation:        0,
			ManagedNames:      []string{},
			Environment:       map[string]string{},
			EnvironmentSHA256: EnvironmentSHA256(map[string]string{}),
		}
		if err := Write(s.path, state); err != nil {
			return Status{}, err
		}
	}
	s.requireState = true
	return statusFor(state, true), nil
}

// normalizeStatePermissions repairs group-only access introduced by the pod
// init container while refusing symlinks, nonregular files, and any file that
// was exposed to other users. Read remains strict for every runtime reload.
func normalizeStatePermissions(path string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if errors.Is(err, syscall.ELOOP) {
		return fmt.Errorf("%w: unsafe state file", ErrInvalidState)
	}
	if err != nil {
		return fmt.Errorf("open runtime credential environment state for permission repair: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return fmt.Errorf("inspect runtime credential environment state for permission repair: %w", statErr)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o007 != 0 {
		_ = file.Close()
		return fmt.Errorf("%w: unsafe state file", ErrInvalidState)
	}
	if info.Mode().Perm() != 0o600 {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return fmt.Errorf("repair runtime credential environment state permissions: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close runtime credential environment state after permission repair: %w", err)
	}
	return nil
}

// Get returns non-secret capability and synchronization status.
func (s *Store) Get() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, seeded, err := Read(s.path)
	if err != nil {
		return Status{}, err
	}
	if !seeded && s.requireState {
		return Status{}, fmt.Errorf("%w: configured state file is missing", ErrInvalidState)
	}
	return statusFor(state, seeded), nil
}

// Put atomically replaces the current environment with one newer generation.
// ManagedNames accumulate so a detached key inherited when the pod started is
// removed from every future agent subprocess.
func (s *Store) Put(generation uint64, environment map[string]string) (Status, error) {
	environment, err := validateAndCloneEnvironment(environment)
	if err != nil {
		return Status{}, err
	}
	if generation == 0 && len(environment) != 0 {
		return Status{}, fmt.Errorf("%w: generation zero requires an empty environment", ErrInvalidEnvironment)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, seeded, err := Read(s.path)
	if err != nil {
		return Status{}, err
	}
	if !seeded && s.requireState {
		return Status{}, fmt.Errorf("%w: configured state file is missing", ErrInvalidState)
	}
	if seeded {
		switch {
		case generation < current.Generation:
			return Status{}, ErrStaleGeneration
		case generation == current.Generation:
			if equalEnvironment(current.Environment, environment) {
				return statusFor(current, true), nil
			}
			return Status{}, ErrGenerationConflict
		}
	}

	managed := make(map[string]struct{}, len(current.ManagedNames)+len(environment))
	for _, name := range current.ManagedNames {
		managed[name] = struct{}{}
	}
	for name := range environment {
		if _, alreadyManaged := managed[name]; !alreadyManaged {
			managed[name] = struct{}{}
		}
	}
	if len(managed) > maxManagedNames {
		return Status{}, fmt.Errorf("%w: too many managed names", ErrInvalidEnvironment)
	}

	managedNames := make([]string, 0, len(managed))
	for name := range managed {
		managedNames = append(managedNames, name)
	}
	slices.Sort(managedNames)
	state := State{
		Kind:              Kind,
		Generation:        generation,
		ManagedNames:      managedNames,
		Environment:       environment,
		EnvironmentSHA256: EnvironmentSHA256(environment),
	}
	if err := Write(s.path, state); err != nil {
		return Status{}, err
	}
	return statusFor(state, true), nil
}

// Read loads one complete state snapshot. A missing file means this runtime
// has not opted into dynamic credential environments and preserves legacy
// inherited-environment behavior.
func Read(path string) (State, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("read runtime credential environment state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return State{}, false, fmt.Errorf("%w: unsafe state file", ErrInvalidState)
	}
	if info.Size() > maxStateBytes {
		return State{}, false, fmt.Errorf("%w: state file is too large", ErrInvalidState)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, false, fmt.Errorf("read runtime credential environment state: %w", err)
	}
	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, false, fmt.Errorf("%w: decode state", ErrInvalidState)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return State{}, false, fmt.Errorf("%w: decode state", ErrInvalidState)
	}
	if err := validateState(state); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

// Write durably replaces one state snapshot without exposing a partially
// written document to concurrent worker readers.
func Write(path string, state State) error {
	if err := validateState(state); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode runtime credential environment state: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxStateBytes {
		return fmt.Errorf("%w: state file is too large", ErrInvalidState)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime credential environment directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".credential-environment-*")
	if err != nil {
		return fmt.Errorf("create runtime credential environment state: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect runtime credential environment state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write runtime credential environment state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync runtime credential environment state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close runtime credential environment state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace runtime credential environment state: %w", err)
	}
	removeTemporary = false
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open runtime credential environment directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync runtime credential environment directory: %w", err)
	}
	return nil
}

// Apply removes every historically managed name from base and overlays the
// current environment snapshot. It returns one entry per environment name.
func Apply(base []string, state State) []string {
	managed := make(map[string]struct{}, len(state.ManagedNames))
	for _, name := range state.ManagedNames {
		managed[name] = struct{}{}
	}
	merged := make(map[string]string, len(base)+len(state.Environment))
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, remove := managed[name]; remove {
			continue
		}
		merged[name] = value
	}
	for name, value := range state.Environment {
		merged[name] = value
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	slices.Sort(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+merged[name])
	}
	return environment
}

// EnvironmentSHA256 returns the canonical digest used by Cloud to compare a
// desired full snapshot without retrieving its values from telosd.
func EnvironmentSHA256(environment map[string]string) string {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	slices.Sort(names)
	hasher := sha256.New()
	var length [4]byte
	for _, name := range names {
		value := environment[name]
		binary.BigEndian.PutUint32(length[:], uint32(len([]byte(name))))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(name))
		binary.BigEndian.PutUint32(length[:], uint32(len([]byte(value))))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func validateState(state State) error {
	if state.Kind != Kind {
		return fmt.Errorf("%w: unsupported state identity", ErrInvalidState)
	}
	environment, err := validateAndCloneEnvironment(state.Environment)
	if err != nil {
		return fmt.Errorf("%w: environment", ErrInvalidState)
	}
	if state.Generation == 0 && len(environment) != 0 {
		return fmt.Errorf("%w: generation zero has a non-empty environment", ErrInvalidState)
	}
	if len(state.ManagedNames) > maxManagedNames {
		return fmt.Errorf("%w: too many managed names", ErrInvalidState)
	}
	managed := make(map[string]struct{}, len(state.ManagedNames))
	for index, name := range state.ManagedNames {
		if err := validateEnvironmentName(name); err != nil {
			return fmt.Errorf("%w: managed name", ErrInvalidState)
		}
		if index > 0 && state.ManagedNames[index-1] >= name {
			return fmt.Errorf("%w: managed names are not canonical", ErrInvalidState)
		}
		managed[name] = struct{}{}
	}
	for name := range environment {
		if _, ok := managed[name]; !ok {
			return fmt.Errorf("%w: current name is not managed", ErrInvalidState)
		}
	}
	if state.EnvironmentSHA256 != EnvironmentSHA256(environment) {
		return fmt.Errorf("%w: environment digest mismatch", ErrInvalidState)
	}
	return nil
}

func validateAndCloneEnvironment(environment map[string]string) (map[string]string, error) {
	if environment == nil {
		return nil, fmt.Errorf("%w: environment must be an object", ErrInvalidEnvironment)
	}
	if len(environment) > maxEnvironmentEntries {
		return nil, fmt.Errorf("%w: too many entries", ErrInvalidEnvironment)
	}
	cloned := make(map[string]string, len(environment))
	for name, value := range environment {
		if err := validateEnvironmentName(name); err != nil {
			return nil, err
		}
		if len(value) > maxEnvironmentValue || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("%w: value for %s is invalid", ErrInvalidEnvironment, name)
		}
		cloned[name] = value
	}
	return cloned, nil
}

func validateEnvironmentName(name string) error {
	upper := strings.ToUpper(name)
	if !environmentName.MatchString(name) {
		return fmt.Errorf("%w: name is invalid", ErrInvalidEnvironment)
	}
	if strings.HasPrefix(upper, "TELOS_") {
		return fmt.Errorf("%w: name %s is reserved", ErrInvalidEnvironment, name)
	}
	if _, reserved := reservedEnvironmentNames[upper]; reserved {
		return fmt.Errorf("%w: name %s is reserved", ErrInvalidEnvironment, name)
	}
	return nil
}

func statusFor(state State, seeded bool) Status {
	if !seeded {
		return Status{
			Supported:         true,
			EnvironmentSHA256: EnvironmentSHA256(map[string]string{}),
		}
	}
	return Status{
		Supported:         true,
		Seeded:            true,
		Generation:        state.Generation,
		Count:             len(state.Environment),
		EnvironmentSHA256: state.EnvironmentSHA256,
	}
}

func equalEnvironment(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		rightValue, ok := right[name]
		if !ok || rightValue != value {
			return false
		}
	}
	return true
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}
