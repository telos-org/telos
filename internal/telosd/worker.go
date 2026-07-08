package telosd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
	"github.com/telos-org/telos/internal/sessionworker"
)

const controllerFailureBackoffCap = 15 * time.Minute

func RunSessionWorker(sessionDir string, once bool) (int, error) {
	var err error
	sessionDir, err = filepath.Abs(sessionDir)
	if err != nil {
		return 1, err
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	wake := make(chan os.Signal, 1)
	signal.Notify(wake, syscall.SIGUSR1)
	defer signal.Stop(wake)

	owner, err := sessionworker.AcquireOwnership(sessionDir, filepath.Join(sessionDir, "runner.log"))
	if err != nil {
		if errors.Is(err, sessionworker.ErrWorkerAlreadyRunning) {
			return 0, nil
		}
		return 1, err
	}
	defer wakeParent(sessionDir)
	defer clearRunner(sessionDir, os.Getpid())
	defer owner.Release()

	failures := 0
	for {
		manifest, err := LoadWorkerManifest(sessionDir)
		if err != nil {
			return 1, err
		}
		root := manifest.Kind == sessionapi.KindController
		desired := manifest.Desired
		result, err := cli.RunLocalSession(sessionDir)
		if err != nil {
			if !root || once {
				return 1, err
			}
			fmt.Fprintf(os.Stderr, "root session cycle failed: %v\n", err)
			failures++
			if waitForNextCycle(wake, stop, failureBackoff(failures)) {
				return 0, nil
			}
			continue
		} else if !root {
			if result.GameResult == game.GameSuccess {
				return 0, nil
			}
			if result.Error != "" {
				return 1, fmt.Errorf("%s", result.Error)
			}
			return 1, fmt.Errorf("session failed: %s", result.GameResult)
		} else if once {
			return 0, nil
		} else if result.GameResult == game.GameStopped {
			return 0, nil
		} else if result.GameResult != game.GameSuccess {
			failures++
			if result.Error != "" {
				fmt.Fprintf(os.Stderr, "root session cycle failed: %s\n", result.Error)
			} else {
				fmt.Fprintf(os.Stderr, "root session cycle failed: %s\n", result.GameResult)
			}
			if waitForNextCycle(wake, stop, failureBackoff(failures)) {
				return 0, nil
			}
			continue
		}
		failures = 0
		if root {
			current, err := LoadWorkerManifest(sessionDir)
			if err == nil && !current.Desired.Equal(desired) {
				drainWake(wake)
				continue
			}
		}
		if waitForNextCycle(wake, stop, controllerInterval(manifest.Interval)) {
			return 0, nil
		}
	}
}

type WorkerManifest struct {
	Kind     sessionapi.SessionKind
	Interval time.Duration
	Desired  DesiredState
}

type DesiredState struct {
	SpecVersion   int
	PackageDigest string
}

func (d DesiredState) Equal(other DesiredState) bool {
	return d.SpecVersion == other.SpecVersion && d.PackageDigest == other.PackageDigest
}

func LoadWorkerManifest(sessionDir string) (WorkerManifest, error) {
	m, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return WorkerManifest{}, fmt.Errorf("read worker manifest: %w", err)
	}
	if m.SessionKind != sessionapi.KindController && m.SessionKind != sessionapi.KindTask {
		return WorkerManifest{}, fmt.Errorf("invalid session_kind %q in worker manifest", m.SessionKind)
	}
	manifest := WorkerManifest{
		Kind: m.SessionKind,
		Desired: DesiredState{
			SpecVersion:   intValue(m.CurrentSpecVersion),
			PackageDigest: strValue(m.PackageDigest),
		},
	}
	if len(m.Specs) == 0 {
		return manifest, nil
	}
	seconds := m.Specs[0].IntervalSeconds
	if seconds == nil || *seconds <= 0 {
		return manifest, nil
	}
	manifest.Interval = time.Duration(*seconds) * time.Second
	return manifest, nil
}

func controllerInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return interval
}

func failureBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	seconds := 1 << min(failures-1, 6)
	backoff := time.Duration(seconds) * time.Second
	if backoff > controllerFailureBackoffCap {
		return controllerFailureBackoffCap
	}
	return backoff
}

func waitForNextCycle(wake <-chan os.Signal, stop <-chan os.Signal, delay time.Duration) bool {
	if delay <= 0 {
		select {
		case <-wake:
			return false
		case <-stop:
			return true
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return false
	case <-wake:
		return false
	case <-stop:
		return true
	}
}

func drainWake(wake <-chan os.Signal) {
	for {
		select {
		case <-wake:
		default:
			return
		}
	}
}

func clearRunner(sessionDir string, pid int) {
	_, _ = sessionapi.MutateManifest(filepath.Join(sessionDir, "session.json"), func(m *sessionapi.Manifest) error {
		if runnerPID, ok := m.Runner.ProcessID(); ok && runnerPID == pid {
			m.Runner = nil
		}
		return nil
	})
}

func wakeParent(sessionDir string) {
	manifest, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil || manifest.ParentSessionID == nil || *manifest.ParentSessionID == "" {
		return
	}
	parentDir := filepath.Join(filepath.Dir(sessionDir), *manifest.ParentSessionID)
	_ = sessionworker.Wake(parentDir)
}

func strValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
