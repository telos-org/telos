package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/telos-org/telos/internal/executor"
	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/sessionapi"
)

// sessionInferenceExecutor bridges the durable settings request to the Pi child
// owned by this worker. The notification handler is joined before the worker
// hands notifications to its idle loop or starts another turn.
type sessionInferenceExecutor struct {
	sessionDir    string
	pi            *executor.PiExecutor
	notifications <-chan struct{}
}

func (e *sessionInferenceExecutor) WorkspaceState() string { return e.pi.WorkspaceState() }

func (e *sessionInferenceExecutor) CheckpointWorkspace(dest string) bool {
	return e.pi.CheckpointWorkspace(dest)
}

func (e *sessionInferenceExecutor) ExecuteTurn(task, role string, ts *game.TurnState) game.TurnResult {
	path := manifestPath(e.sessionDir)
	if err := sessionapi.RecoverInferenceUpdate(path); err != nil {
		return game.TurnResult{Role: role, Error: fmt.Sprintf("recover inference settings: %v", err)}
	}
	m, err := sessionapi.ReadManifest(path)
	if err != nil {
		return game.TurnResult{Role: role, Error: fmt.Sprintf("read inference settings: %v", err)}
	}
	if m.Config.Model != "" {
		e.pi.Model = m.Config.Model
	}
	if m.Config.Thinking != "" {
		e.pi.Thinking = m.Config.Thinking
	}
	e.pi.ModelConfigPath = filepath.Join(e.sessionDir, "inference-model.json")
	provider, _, _ := strings.Cut(e.pi.Model, "/")
	if err := executor.WritePiModelDefinition(e.pi.ModelConfigPath, provider, m.InferenceModelDefinition); err != nil {
		return game.TurnResult{Role: role, Error: fmt.Sprintf("restore inference model definition: %v", err)}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, done := make(chan struct{}), make(chan struct{})
	var stop atomic.Bool
	var controlErr error
	blocked := false
	apply := func(ctx context.Context) error {
		if blocked {
			return nil
		}
		uncertain, err := applyInferenceUpdate(ctx, path, e.pi)
		blocked = uncertain
		return err
	}
	e.pi.BeforePrompt = func(ctx context.Context) error {
		err := apply(ctx)
		close(ready)
		return err
	}
	go func() {
		defer close(done)
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.notifications:
				if err := apply(ctx); err != nil {
					controlErr = err
					stop.Store(true)
					return
				}
			}
		}
	}()
	state := game.TurnState{}
	if ts != nil {
		state = *ts
	}
	previousStop := state.StopRequested
	state.StopRequested = func() bool { return stop.Load() || (previousStop != nil && previousStop()) }
	result := e.pi.ExecuteTurn(task, role, &state)
	cancel()
	<-done
	e.pi.BeforePrompt = nil
	if controlErr != nil {
		result.Error = fmt.Sprintf("persist inference change: %v", controlErr)
	}
	return result
}

type inferenceController interface {
	SetSettings(context.Context, executor.PiSettingsUpdate) (executor.PiSettings, error)
}

func applyInferenceUpdate(ctx context.Context, path string, pi inferenceController) (bool, error) {
	m, err := sessionapi.ReadManifest(path)
	if err != nil {
		return false, err
	}
	if m.InferenceUpdate == nil || m.InferenceUpdate.Status != "pending" {
		return false, nil
	}
	update, err := sessionapi.ClaimInferenceUpdate(path)
	if err != nil || update == nil {
		return false, err
	}
	desired := executor.PiSettingsUpdate{ModelDefinition: update.ModelDefinition}
	if update.Model != nil {
		desired.Provider, desired.Model, _ = strings.Cut(*update.Model, "/")
	}
	if update.Thinking != nil {
		desired.Thinking = *update.Thinking
	}
	settings, err := pi.SetSettings(ctx, desired)
	partial := errors.Is(err, executor.ErrPiSettingsPartiallyApplied)
	if err != nil && !partial {
		status := "unknown"
		if errors.Is(err, executor.ErrPiLiveSettingsUnsupported) || errors.Is(err, executor.ErrPiSettingsRejected) {
			status = "rejected"
		}
		return status == "unknown", sessionapi.FinishInferenceUpdate(path, update.RequestID, status, err.Error(), nil)
	}
	if settings.Provider == "" || settings.Model == "" || settings.Thinking == "" {
		return true, sessionapi.FinishInferenceUpdate(path, update.RequestID, "unknown", "Pi returned incomplete settings", nil)
	}
	confirmed := sessionapi.InferenceSettings{Model: settings.Provider + "/" + settings.Model, Thinking: settings.Thinking}
	if (partial && (update.Model == nil || update.Thinking == nil)) ||
		(update.Model != nil && *update.Model != confirmed.Model) ||
		(update.Model == nil && m.Config.Model != confirmed.Model) ||
		(!partial && update.Thinking != nil && *update.Thinking != confirmed.Thinking) {
		return true, sessionapi.FinishInferenceUpdate(path, update.RequestID, "unknown", "Pi returned different settings than requested", nil)
	}
	if partial {
		return false, sessionapi.FinishInferenceUpdate(path, update.RequestID, "partial", err.Error(), &confirmed)
	}
	return false, sessionapi.FinishInferenceUpdate(path, update.RequestID, "applied", "", &confirmed)
}
