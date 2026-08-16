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
	safePoint, err := checkpointSafePointForWorker()
	if err != nil {
		return 1, fmt.Errorf("initialize checkpoint safe point: %w", err)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	wake := make(chan os.Signal, 1)
	signal.Notify(wake, syscall.SIGUSR1)
	defer signal.Stop(wake)

	lease, stopped, err := waitForCheckpointAdmission(safePoint, wake, stop)
	if err != nil {
		return 1, err
	}
	if stopped {
		return 0, nil
	}
	owner, err := sessionworker.AcquireOwnership(sessionDir, filepath.Join(sessionDir, "runner.log"))
	if err != nil {
		lease.release()
		if errors.Is(err, sessionworker.ErrWorkerAlreadyRunning) {
			return 0, nil
		}
		return 1, err
	}
	defer func() {
		if lease == nil && safePoint != nil {
			cleanupLease, _, cleanupErr := waitForCheckpointAdmission(safePoint, nil, nil)
			if cleanupErr == nil {
				lease = cleanupLease
			}
		}
		finishCheckpointWork(lease, func() {
			clearRunner(sessionDir, os.Getpid())
			_ = owner.Release()
			wakeParent(sessionDir)
		})
	}()

	failures := 0
	for {
		if lease == nil {
			lease, stopped, err = waitForCheckpointAdmission(safePoint, wake, stop)
			if err != nil {
				return 1, err
			}
			if stopped {
				return 0, nil
			}
		}
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
			lease.release()
			lease = nil
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
			lease.release()
			lease = nil
			if waitForNextCycle(wake, stop, failureBackoff(failures)) {
				return 0, nil
			}
			continue
		}
		failures = 0
		if root {
			completedDesired := desired
			if completed, ok, err := LoadCompletedEpochDesired(sessionDir); err == nil && ok {
				completedDesired = completed
			}
			current, err := LoadWorkerManifest(sessionDir)
			if err == nil && !current.Desired.Equal(completedDesired) {
				drainWake(wake)
				if stopRequested(stop) {
					return 0, nil
				}
				lease.release()
				lease = nil
				continue
			}
		}
		lease.release()
		lease = nil
		if waitForNextCycle(wake, stop, controllerInterval(manifest.Interval)) {
			return 0, nil
		}
	}
}

func finishCheckpointWork(lease *checkpointLease, cleanup func()) {
	defer lease.release()
	if cleanup != nil {
		cleanup()
	}
}

func waitForCheckpointAdmission(safePoint *checkpointSafePoint, wake, stop <-chan os.Signal) (*checkpointLease, bool, error) {
	if safePoint == nil {
		return nil, false, nil
	}
	for {
		lease, err := safePoint.acquire()
		if err == nil {
			return lease, false, nil
		}
		if !errors.Is(err, errCheckpointAdmissionClosed) {
			return nil, false, fmt.Errorf("acquire checkpoint work lease: %w", err)
		}
		if waitForCheckpointRetry(safePoint.pollInterval, wake, stop) {
			return nil, true, nil
		}
	}
}

// Go 1.23 and later timer channels are synchronous. Stopping and then draining
// a timer can therefore block forever if the timer became ready concurrently
// but the signal case won the select.
func waitForCheckpointRetry(delay time.Duration, wake, stop <-chan os.Signal) bool {
	timer := time.NewTimer(delay)
	select {
	case <-timer.C:
		return false
	case <-wake:
		timer.Stop()
		return false
	case <-stop:
		timer.Stop()
		return true
	}
}

func stopRequested(stop <-chan os.Signal) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// LoadCompletedEpochDesired returns the immutable desired identity bound to
// the most recently completed epoch. Legacy epochs do not carry this identity.
func LoadCompletedEpochDesired(sessionDir string) (DesiredState, bool, error) {
	m, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return DesiredState{}, false, fmt.Errorf("read worker manifest: %w", err)
	}
	epoch := m.LastEpoch()
	if epoch == nil || epoch.FinishedAt == nil || epoch.SpecVersion == nil {
		return DesiredState{}, false, nil
	}
	return DesiredState{
		SpecVersion:   *epoch.SpecVersion,
		PackageDigest: strValue(epoch.PackageDigest),
	}, true, nil
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
