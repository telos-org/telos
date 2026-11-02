package telosd

import (
	"cmp"
	"errors"
	"fmt"
	"math/rand/v2"
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

// holdRecheckInterval is how often a held session rechecks its goal's hold.
const holdRecheckInterval = 5 * time.Second

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
	return runSessionWorker(sessionDir, once, cli.RunLocalSession, wake, stop)
}

func runSessionWorker(
	sessionDir string,
	once bool,
	runSession func(string) (*game.PVGResult, error),
	wake <-chan os.Signal,
	stop <-chan os.Signal,
) (int, error) {
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
		if stopRequested(stop) {
			return 0, nil
		}
		manifest, err := LoadWorkerManifest(sessionDir)
		if err != nil {
			return 1, err
		}
		root := manifest.Kind == sessionapi.KindController
		if !once && manifest.OnHold {
			cli.ClearRetryingIssue(sessionDir) // stopped agents are not retrying
			failures = 0
			if waitOutHold(sessionDir, root, wake, stop) {
				return 0, nil
			}
			continue
		}
		desired := manifest.Desired
		result, err := runSession(sessionDir)
		if err == nil && result.GameResult == game.GameStopped && !once {
			// Agents stopped for a hold, even one lifted since, go round again;
			// an operator stop exits.
			if current, err := LoadWorkerManifest(sessionDir); err == nil && !current.Stopped {
				continue
			}
		}
		if err != nil {
			if !root || once {
				return 1, err
			}
			fmt.Fprintf(os.Stderr, "root session cycle failed: %v\n", err)
			failures++
			if waitForRetry(sessionDir, wake, stop, failures, sessionapi.ReasonInternalError, "") {
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
		} else if result.GameResult == game.GameBlocked {
			continue // the game recorded the block; the next loop waits it out
		} else if result.GameResult != game.GameSuccess {
			failures++
			fmt.Fprintf(os.Stderr, "root session cycle failed: %s\n", cmp.Or(result.Error, string(result.GameResult)))
			reason := sessionapi.ReasonInternalError
			if result.ProviderError != "" {
				reason = sessionapi.ReasonInferenceProviderError
			}
			if waitForRetry(sessionDir, wake, stop, failures, reason, result.ProviderError) {
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
				continue
			}
		}
		if waitForNextCycle(wake, stop, controllerInterval(manifest.Interval)) {
			return 0, nil
		}
	}
}

// waitOutHold waits for the goal's hold to lift and reports a stop. It
// rechecks on a timer rather than relying on a wake, which can be lost. An
// accepted root then keeps its schedule instead of starting a check at once.
func waitOutHold(sessionDir string, root bool, wake, stop <-chan os.Signal) bool {
	for {
		if waitForNextCycle(wake, stop, holdRecheckInterval) {
			return true
		}
		manifest, err := LoadWorkerManifest(sessionDir)
		if err == nil && manifest.OnHold {
			continue
		}
		if err == nil && root && manifest.Accepted {
			return waitForNextCycle(wake, stop, controllerInterval(manifest.Interval))
		}
		return false
	}
}

// waitForRetry records a retrying issue for the failed attempt and waits out
// its backoff. A wake (new settings, spec, or lifted hold) retries at once.
// message is a provider's own words, or empty: Telos errors stay in the log.
func waitForRetry(sessionDir string, wake, stop <-chan os.Signal, failures int, reason, message string) bool {
	delay := jitteredFailureBackoff(failures, time.Second)
	now := time.Now()
	_, err := sessionapi.MutateManifest(filepath.Join(sessionDir, "session.json"), func(m *sessionapi.Manifest) error {
		if m.Issue.Blocked() {
			return nil // only Resume or a new spec version clears a block
		}
		issue := sessionapi.NewIssue(sessionapi.IssueRetrying, reason, message, now)
		if m.Issue != nil && m.Issue.Reason == reason {
			issue.Since = m.Issue.Since
		}
		issue.LastRetryAt = sessionapi.FormatTime(now)
		issue.NextRetryAt = sessionapi.FormatTime(now.Add(delay))
		m.Issue = issue
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "record root session issue: %v\n", err)
	}
	return waitForNextCycle(wake, stop, delay)
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
	OnHold   bool
	Accepted bool
	Stopped  bool
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
	root, _ := sessionapi.ReadRootManifest(sessionDir)
	manifest := WorkerManifest{
		Kind:     m.SessionKind,
		OnHold:   root.OnHold(),
		Accepted: m.Accepted(),
		Stopped:  m.IsStopped(),
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

func failureBackoff(failures int, initial time.Duration) time.Duration {
	multiplier := 1 << min(max(failures-1, 0), 20)
	return min(initial*time.Duration(multiplier), controllerFailureBackoffCap)
}

func jitteredFailureBackoff(failures int, initial time.Duration) time.Duration {
	base := failureBackoff(failures, initial)
	window := base / 5
	if window <= 0 {
		return base
	}
	return base - window + time.Duration(rand.Int64N(int64(window)+1))
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
