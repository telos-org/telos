package telosd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/telos-org/telos/internal/cli"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
)

func RunSessionWorker(sessionDir string, once bool) (int, error) {
	var err error
	sessionDir, err = filepath.Abs(sessionDir)
	if err != nil {
		return 1, err
	}
	manifest, err := LoadWorkerManifest(sessionDir)
	if err != nil {
		return 1, err
	}
	root := manifest.Kind == sessionapi.KindController
	reconciledTerminal := false
	reconcileTerminal := func() {
		if reconciledTerminal {
			return
		}
		reconciledTerminal = true
		reconcileWorkerBilling(sessionDir, manifest, true)
	}
	if !root || once {
		defer reconcileTerminal()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	for {
		result, err := cli.RunLocalSession(sessionDir)
		if err != nil {
			if !root || once {
				return 1, err
			}
			fmt.Fprintf(os.Stderr, "root session cycle failed: %v\n", err)
		} else if !root {
			reconcileTerminal()
			if result.GameResult == game.GameSuccess {
				return 0, nil
			}
			if result.Error != "" {
				return 1, fmt.Errorf("%s", result.Error)
			}
			return 1, fmt.Errorf("session failed: %s", result.GameResult)
		} else if once {
			reconcileTerminal()
			return 0, nil
		} else {
			reconcileWorkerBilling(sessionDir, manifest, false)
		}
		if root && manifest.Interval <= 0 {
			<-stop
			return 0, nil
		}
		timer := time.NewTimer(manifest.Interval)
		select {
		case <-timer.C:
		case <-stop:
			timer.Stop()
			return 0, nil
		}
	}
}

type WorkerManifest struct {
	SessionID string
	Kind      sessionapi.SessionKind
	Runtime   sessionapi.SessionRuntime
	Interval  time.Duration
}

func LoadWorkerManifest(sessionDir string) (WorkerManifest, error) {
	m, err := sessionapi.ReadManifest(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return WorkerManifest{}, fmt.Errorf("read worker manifest: %w", err)
	}
	if m.SessionKind != sessionapi.KindController && m.SessionKind != sessionapi.KindTask {
		return WorkerManifest{}, fmt.Errorf("invalid session_kind %q in worker manifest", m.SessionKind)
	}
	manifest := WorkerManifest{SessionID: m.SessionID, Kind: m.SessionKind, Runtime: m.Runtime}
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

func reconcileWorkerBilling(sessionDir string, manifest WorkerManifest, terminal bool) {
	if manifest.Runtime != sessionapi.RuntimeCloud {
		return
	}
	if !managedGatewayModeEnabled() {
		return
	}
	sessionID := manifest.SessionID
	if sessionID == "" {
		sessionID = filepath.Base(sessionDir)
	}
	client := newBillingClient(workerBillingConfigFromEnv())
	if !client.configured() {
		return
	}
	if err := client.ReconcileSession(sessionID, terminal); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reconcile managed billing: %v\n", err)
	}
}

func workerBillingConfigFromEnv() BillingConfig {
	cfg := BillingConfig{
		Endpoint:  envOr("TELOS_BILLING_ENDPOINT", "https://billing.usetelos.ai"),
		EnvID:     os.Getenv("TELOS_ENV_ID"),
		TokenFile: os.Getenv("TELOS_BILLING_ENV_TOKEN_FILE"),
		Token:     os.Getenv("TELOS_BILLING_ENV_TOKEN"),
	}
	if cfg.Token == "" && cfg.TokenFile != "" {
		if token, err := authTokenFromFile(cfg.TokenFile); err == nil {
			cfg.Token = token
		}
	}
	return cfg
}
