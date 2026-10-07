package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// ErrPiNotRunning means there is no active Pi invocation to configure.
var ErrPiNotRunning = errors.New("pi is not running")

// ErrPiLiveSettingsUnsupported rejects a control without changing or stopping the
// active invocation. Ordinary runs do not require the live-settings capability.
var ErrPiLiveSettingsUnsupported = fmt.Errorf("live model/thinking changes require Pi %s", piLiveSettingsVersion)

// ErrPiSettingsRejected means validation rejected a change before mutation.
var ErrPiSettingsRejected = errors.New("pi settings rejected")

// ErrPiSettingsPartiallyApplied accompanies confirmed settings when the model
// was set but thinking validation rejected the second part before mutation.
var ErrPiSettingsPartiallyApplied = errors.New("pi settings partially applied")

const piCommandTimeout = 30 * time.Second
const piShutdownTimeout = 5 * time.Second

// PiSettings describes Pi's configured settings. A request already in flight
// keeps its settings; these values apply to subsequent requests.
type PiSettings struct {
	Provider string
	Model    string
	Thinking string
}

type PiSettingsUpdate struct {
	Provider        string
	Model           string
	Thinking        string
	ModelDefinition json.RawMessage
}

// SetSettings serializes one requested update and reads back its actual result.
// Model is applied before thinking. Pi may start inference between commands;
// this is one tracked operation, not an atomic change between model requests.
func (pe *PiExecutor) SetSettings(ctx context.Context, update PiSettingsUpdate) (PiSettings, error) {
	if (update.Provider == "") != (update.Model == "") || (update.Model == "" && update.Thinking == "") || (update.Model == "" && len(update.ModelDefinition) > 0) {
		return PiSettings{}, fmt.Errorf("%w: provide a model and/or thinking level", ErrPiSettingsRejected)
	}
	settings, err := pe.configure(ctx, func(ctx context.Context, rpc *piRPC) error {
		if update.Model != "" {
			if len(update.ModelDefinition) > 0 {
				if err := pe.registerModelDefinition(ctx, rpc, update.Provider, update.ModelDefinition); err != nil {
					return err
				}
			}
			if err := rpc.setModel(ctx, update.Provider, update.Model); err != nil {
				return err
			}
		}
		if update.Thinking != "" {
			err := rpc.setThinkingLevel(ctx, update.Thinking)
			if update.Model != "" && errors.Is(err, ErrPiSettingsRejected) {
				return fmt.Errorf("%w: model set, but %v", ErrPiSettingsPartiallyApplied, err)
			}
			return err
		}
		return nil
	})
	if errors.Is(err, ErrPiSettingsPartiallyApplied) && (settings.Provider != update.Provider || settings.Model != update.Model) {
		return PiSettings{}, errors.New("Pi returned a different model after a partial settings change")
	}
	return settings, err
}

// SetModel changes the active invocation's model without restarting it or its
// tools. Pi may also adjust thinking for the selected model. The returned values
// reflect Pi's configuration, not the settings of a request already in flight.
// Changes are invocation-local; callers must persist defaults for future turns.
// Versions without verified invocation-local controls return
// ErrPiLiveSettingsUnsupported and continue running with their original settings.
// Cancellation stops waiting, not a command Pi has already accepted. A timed-out
// command may still apply later; callers must not automatically retry it.
func (pe *PiExecutor) SetModel(ctx context.Context, provider, model string) (PiSettings, error) {
	return pe.SetSettings(ctx, PiSettingsUpdate{Provider: provider, Model: model})
}

func (rpc *piRPC) setModel(ctx context.Context, provider, model string) error {
	data, err := rpc.call(ctx, "get_available_models", nil)
	if err != nil {
		return err
	}
	var available struct {
		Models []struct{ Provider, ID string }
	}
	if err := json.Unmarshal(data, &available); err != nil {
		return err
	}
	for _, candidate := range available.Models {
		if candidate.Provider == provider && candidate.ID == model {
			_, err := rpc.call(ctx, "set_model", map[string]interface{}{"provider": provider, "modelId": model})
			return err
		}
	}
	return fmt.Errorf("%w: Model not found: %s/%s", ErrPiSettingsRejected, provider, model)
}

// SetThinkingLevel changes the active invocation's thinking level. Unsupported
// values are rejected using Pi's own model-specific list, rather than clamped.
// As with SetModel, a command may still apply after its caller stops waiting.
func (pe *PiExecutor) SetThinkingLevel(ctx context.Context, level string) (PiSettings, error) {
	return pe.SetSettings(ctx, PiSettingsUpdate{Thinking: level})
}

func (rpc *piRPC) setThinkingLevel(ctx context.Context, level string) error {
	data, err := rpc.call(ctx, "get_available_thinking_levels", nil)
	if err != nil {
		return err
	}
	var available struct{ Levels []string }
	if err := json.Unmarshal(data, &available); err != nil {
		return fmt.Errorf("pi thinking levels: %w", err)
	}
	for _, supported := range available.Levels {
		if level == supported {
			_, err := rpc.call(ctx, "set_thinking_level", map[string]interface{}{"level": level})
			return err
		}
	}
	return fmt.Errorf("%w: unsupported thinking level %q; available: %v", ErrPiSettingsRejected, level, available.Levels)
}

func (pe *PiExecutor) configure(ctx context.Context, apply func(context.Context, *piRPC) error) (PiSettings, error) {
	ctx, cancel := context.WithTimeout(ctx, piCommandTimeout)
	defer cancel()
	pe.mu.Lock()
	rpc := pe.active
	pe.mu.Unlock()
	if rpc == nil {
		return PiSettings{}, ErrPiNotRunning
	}
	if err := rpc.requireLiveSettings(ctx); err != nil {
		return PiSettings{}, err
	}
	// Serialize our controls, including validation and readback. Pi itself handles
	// RPC commands concurrently; this does not make two separate updates atomic.
	select {
	case rpc.control <- struct{}{}:
		defer func() { <-rpc.control }()
	case <-ctx.Done():
		return PiSettings{}, ctx.Err()
	case <-rpc.done:
		return PiSettings{}, rpc.closedError()
	}
	applyErr := apply(ctx, rpc)
	if applyErr != nil && !errors.Is(applyErr, ErrPiSettingsPartiallyApplied) {
		return PiSettings{}, applyErr
	}
	data, err := rpc.call(ctx, "get_state", nil)
	if err != nil {
		return PiSettings{}, fmt.Errorf("pi accepted settings but readback failed: %w", err)
	}
	var state struct {
		Model         struct{ Provider, ID string }
		ThinkingLevel string
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return PiSettings{}, fmt.Errorf("pi settings: %w", err)
	}
	if state.Model.Provider == "" || state.Model.ID == "" || state.ThinkingLevel == "" {
		return PiSettings{}, errors.New("Pi returned incomplete settings")
	}
	return PiSettings{Provider: state.Model.Provider, Model: state.Model.ID, Thinking: state.ThinkingLevel}, applyErr
}

type piResponse struct {
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

type piWrite struct {
	ctx  context.Context
	data []byte
	done chan error
}

// piRPC owns only transport and command correlation. One reader feeds responses,
// and one writer keeps JSONL records intact without blocking that reader.
type piRPC struct {
	mu                sync.Mutex
	stdin             io.WriteCloser
	nextID            uint64
	pending           map[string]chan piResponse
	ready             chan struct{}
	done              chan struct{}
	writes            chan piWrite
	control           chan struct{}
	closedAt          time.Time
	err               error
	capabilitiesReady chan struct{}
	liveSettings      bool
}

func newPiRPC() *piRPC {
	return &piRPC{
		pending: make(map[string]chan piResponse),
		ready:   make(chan struct{}), done: make(chan struct{}),
		writes: make(chan piWrite), control: make(chan struct{}, 1),
		capabilitiesReady: make(chan struct{}),
	}
}

// The launcher emits this preamble before exec'ing Pi. Version detection only
// enables audited controls; failed/unknown probes do not reject ordinary runs.
func (rpc *piRPC) setLiveSettingsSupport(supported bool) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	select {
	case <-rpc.capabilitiesReady:
		return
	default:
		rpc.liveSettings = supported
		close(rpc.capabilitiesReady)
	}
}

func (rpc *piRPC) requireLiveSettings(ctx context.Context) error {
	select {
	case <-rpc.capabilitiesReady:
	case <-rpc.done:
		return rpc.closedError()
	case <-ctx.Done():
		return ctx.Err()
	}
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if rpc.err != nil {
		return rpc.err
	}
	if !rpc.liveSettings {
		return ErrPiLiveSettingsUnsupported
	}
	return nil
}

func (rpc *piRPC) start(stdin io.WriteCloser) {
	rpc.mu.Lock()
	if rpc.err != nil {
		rpc.mu.Unlock()
		stdin.Close()
		return
	}
	rpc.stdin = stdin
	close(rpc.ready)
	rpc.mu.Unlock()
	go func() {
		for {
			select {
			case write := <-rpc.writes:
				if err := write.ctx.Err(); err != nil {
					write.done <- err
					continue
				}
				_, err := stdin.Write(write.data)
				write.done <- err
				if err != nil {
					rpc.close(fmt.Errorf("pi RPC write: %w", err))
					return
				}
			case <-rpc.done:
				return
			}
		}
	}()
}

func (rpc *piRPC) call(ctx context.Context, command string, fields map[string]interface{}) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rpc.mu.Lock()
	if rpc.err != nil {
		err := rpc.err
		rpc.mu.Unlock()
		return nil, err
	}
	rpc.nextID++
	id := fmt.Sprintf("telos-%d", rpc.nextID)
	response := make(chan piResponse, 1)
	rpc.pending[id] = response
	rpc.mu.Unlock()
	defer func() {
		rpc.mu.Lock()
		delete(rpc.pending, id)
		rpc.mu.Unlock()
	}()
	if fields == nil {
		fields = make(map[string]interface{})
	}
	fields["id"], fields["type"] = id, command
	var reply piResponse
	err := rpc.send(ctx, fields)
	if err == nil {
		select {
		case reply = <-response:
		case <-ctx.Done():
			err = ctx.Err()
		case <-rpc.done:
			err = rpc.closedError()
		}
	}
	if err != nil {
		// A fast child may reply and settle before the writer resumes. Prefer
		// its authoritative response even when transport closure wins a select.
		select {
		case reply = <-response:
		default:
			return nil, err
		}
	}
	if !reply.Success {
		return nil, fmt.Errorf("pi %s: %s", command, reply.Error)
	}
	return reply.Data, nil
}

func (rpc *piRPC) send(ctx context.Context, record interface{}) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	select {
	case <-rpc.ready:
	case <-rpc.done:
		return rpc.closedError()
	case <-ctx.Done():
		return ctx.Err()
	}
	write := piWrite{ctx: ctx, data: append(data, '\n'), done: make(chan error, 1)}
	select {
	case rpc.writes <- write:
	case <-rpc.done:
		return rpc.closedError()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-write.done:
		return err
	case <-rpc.done:
		return rpc.closedError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (rpc *piRPC) respond(line string) {
	var response piResponse
	if json.Unmarshal([]byte(line), &response) != nil {
		return
	}
	rpc.mu.Lock()
	if ch := rpc.pending[response.ID]; ch != nil {
		select {
		case ch <- response:
		default:
		}
	}
	rpc.mu.Unlock()
}

func (rpc *piRPC) close(err error) {
	rpc.mu.Lock()
	if rpc.err != nil {
		rpc.mu.Unlock()
		return
	}
	rpc.err, rpc.closedAt = err, time.Now()
	close(rpc.done)
	stdin := rpc.stdin
	rpc.mu.Unlock()
	if stdin != nil {
		stdin.Close()
	}
}

func (rpc *piRPC) closedError() error {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	return rpc.err
}

func (rpc *piRPC) shutdownExpired() bool {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	return !rpc.closedAt.IsZero() && time.Since(rpc.closedAt) >= piShutdownTimeout
}
