package telosd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/telos-org/telos-go/internal/cli"
	"github.com/telos-org/telos-go/internal/game"
	"github.com/telos-org/telos-go/internal/sessionapi"
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
	controller := manifest.Kind == sessionapi.KindController
	if controller && !once && manifest.Interval <= 0 {
		return 1, fmt.Errorf("controller session %s has no positive interval_seconds", filepath.Base(sessionDir))
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	for {
		result, err := cli.RunLocalSession(sessionDir)
		if err != nil {
			if !controller || once {
				return 1, err
			}
			fmt.Fprintf(os.Stderr, "controller cycle failed: %v\n", err)
		} else if !controller {
			if result.GameResult == game.GameSuccess {
				return 0, nil
			}
			if result.Error != "" {
				return 1, fmt.Errorf("%s", result.Error)
			}
			return 1, fmt.Errorf("session failed: %s", result.GameResult)
		} else if once {
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
	Kind     sessionapi.SessionKind
	Interval time.Duration
}

func LoadWorkerManifest(sessionDir string) (WorkerManifest, error) {
	data, err := os.ReadFile(filepath.Join(sessionDir, "session.json"))
	if err != nil {
		return WorkerManifest{}, fmt.Errorf("read worker manifest: %w", err)
	}
	var raw struct {
		SessionKind sessionapi.SessionKind `json:"session_kind"`
		Specs       []struct {
			IntervalSeconds *float64 `json:"interval_seconds"`
		} `json:"specs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return WorkerManifest{}, fmt.Errorf("decode worker manifest: %w", err)
	}
	if raw.SessionKind != sessionapi.KindController && raw.SessionKind != sessionapi.KindTask {
		return WorkerManifest{}, fmt.Errorf("invalid session_kind %q in worker manifest", raw.SessionKind)
	}
	manifest := WorkerManifest{Kind: raw.SessionKind}
	if len(raw.Specs) == 0 {
		return manifest, nil
	}
	seconds := raw.Specs[0].IntervalSeconds
	if seconds == nil || *seconds <= 0 {
		return manifest, nil
	}
	manifest.Interval = time.Duration(*seconds * float64(time.Second))
	return manifest, nil
}
