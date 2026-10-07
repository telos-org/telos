package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/game"
	"github.com/telos-org/telos/internal/platform"
)

func TestNewPiExecutorDefaultsToNoTimeout(t *testing.T) {
	exec := NewPiExecutor(nil, "claude-test", "", 0)

	if exec.Timeout != 0 {
		t.Fatalf("timeout should default to disabled, got %d", exec.Timeout)
	}
	if exec.Thinking != "medium" {
		t.Fatalf("thinking: got %q", exec.Thinking)
	}
}

func TestRecoverableAgentFailureRejectsCredentialErrors(t *testing.T) {
	for _, errorText := range []string{
		"403: inactive virtual key",
		"request failed (HTTP 401)",
	} {
		if recoverableAgentFailure(errorText) {
			t.Fatalf("credential error marked recoverable: %q", errorText)
		}
	}
	for _, errorText := range []string{
		"429: provider at capacity",
		"502: no healthy upstream",
	} {
		if !recoverableAgentFailure(errorText) {
			t.Fatalf("transient error marked non-recoverable: %q", errorText)
		}
	}
}

func TestPiLineEventsProjectsSafeToolCallProgress(t *testing.T) {
	events := piLineEvents(`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"read","arguments":{"path":"/tmp/session/spec.md"}},{"type":"toolCall","name":"read","arguments":{"path":"/tmp/other/spec.md"}},{"type":"toolCall","name":"bash","arguments":{"command":"kubectl get pods --token SECRET"}},{"type":"toolCall","name":"bash","arguments":{"command":"git status --short"}}]}}`)

	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Kind+":"+event.Text)
	}
	want := []string{
		"tool:Reading session/spec.md",
		"tool:Reading other/spec.md",
		"tool:Running kubectl",
		"tool:Updating workspace",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\ngot\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(strings.Join(got, "\n"), "SECRET") {
		t.Fatalf("tool progress leaked command contents: %v", got)
	}
}

func TestPiStreamUsesFinalMessageAndAggregatesUsage(t *testing.T) {
	var stream piStream
	for _, line := range []string{
		`{"message":{"role":"assistant","model":"first","stopReason":"error","errorMessage":"overloaded_error","content":[{"type":"text","text":"old <status>CONCEDE</status>"}],"usage":{"input":1,"output":2,"cost":{"total":0.25}}}}`,
		`{"message":{"role":"toolResult"}}`,
		`{"message":{"role":"assistant","model":"second","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"<progress_update>Checking again.</progress_update>"}],"usage":{"input":3,"output":4,"cacheRead":5,"cacheWrite":6,"cost":{"total":0.5}}}}`,
	} {
		var record map[string]interface{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		stream.message(record)
	}
	want := game.TurnStats{Model: "second", NumTurns: 1, InputTokens: 4, OutputTokens: 6, CacheReadTokens: 5, CacheCreationTokens: 6, CostUSD: 0.75}
	if stream.stats != want || stream.err != "" || strings.Contains(stream.logs, "old") || strings.Contains(stream.logs, "hidden") || game.ExtractStatus(stream.logs) != game.StatusContinue {
		t.Fatalf("wrong stream summary: %+v", stream)
	}
}

func TestExecuteTurnRPCCompletion(t *testing.T) {
	for _, tc := range []struct{ scenario, wantError string }{
		{"normal", ""},
		{"retry", ""},
		{"provider_error", "overloaded_error: exhausted"},
		{"truncated", "agent_output_truncated:length"},
		{"aborted", "agent_failed:aborted"},
		{"early_exit", "pi_rpc_exited_before_settled"},
		{"handled", "pi_prompt_handled"},
		{"rejected", "inactive virtual key"},
		{"malformed", "pi_rpc_protocol"},
		{"malformed_then_success", "pi_rpc_protocol"},
		{"legacy_version", ""},
		{"dialog", ""},
		{"stderr", "EROFS: read-only file system"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			pe := helperExecutor(t, tc.scenario)
			result := pe.ExecuteTurn("work", "prover", nil)
			if tc.wantError == "" {
				if result.Error != "" || result.Status != game.StatusConcede || !strings.HasSuffix(result.Logs, "<status>CONCEDE</status>") {
					t.Fatalf("result: status=%s error=%q log bytes=%d", result.Status, result.Error, len(result.Logs))
				}
			} else if !strings.Contains(result.Error, tc.wantError) || result.Status != game.StatusContinue {
				t.Fatalf("wanted error %q: %+v", tc.wantError, result)
			}
			if tc.scenario == "rejected" && result.Recoverable {
				t.Fatal("configuration failure must be terminal")
			}
			if _, err := pe.SetModel(context.Background(), "provider", "other"); !errors.Is(err, ErrPiNotRunning) {
				t.Fatalf("control after exit: %v", err)
			}
		})
	}
}

func TestExecuteTurnRPCTransmitsLargePromptAndSessionOptions(t *testing.T) {
	pe := helperExecutor(t, "normal")
	pe.Platform.Env["TELOS_PI_APPEND_SYSTEM_PROMPT"] = "hosted instructions\nsecond line"
	state := game.NewPVGState("spec", t.TempDir(), "sess").Turn(1, 1, "prover")
	task := strings.Repeat("quotes \" newline\nseparator\u2028 ", 20000)
	result := pe.ExecuteTurn(task, "prover", state)
	if result.Error != "" {
		t.Fatal(result.Error)
	}
	data, err := os.ReadFile(filepath.Join(pe.Platform.Workspace, "prompt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var prompt struct{ Message string }
	if err := json.Unmarshal(data, &prompt); err != nil || prompt.Message != task {
		t.Fatalf("prompt corrupted: %v, bytes=%d", err, len(prompt.Message))
	}
	argsData, err := os.ReadFile(filepath.Join(pe.Platform.Workspace, "args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(argsData, &args); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, "\n"), "--mode\nrpc") || !strings.Contains(strings.Join(args, "\n"), "--session\n"+state.PiSessionPath()) {
		t.Fatalf("wrong args: %v", args)
	}
	for i, arg := range args {
		if arg == "--append-system-prompt" {
			content, err := os.ReadFile(args[i+1])
			if err != nil || string(content) != pe.Platform.Env["TELOS_PI_APPEND_SYSTEM_PROMPT"] {
				t.Fatalf("append prompt: %q, %v", content, err)
			}
			os.Remove(args[i+1])
			return
		}
	}
	t.Fatal("missing appended system prompt")
}

func TestExecuteTurnRPCControls(t *testing.T) {
	pe := helperExecutor(t, "controls")
	started := make(chan struct{}, 1)
	state := &game.TurnState{Dir: t.TempDir(), OnLiveEvent: func(e game.LiveAgentEvent) { started <- struct{}{} }}
	resultCh := make(chan game.TurnResult, 1)
	go func() { resultCh <- pe.ExecuteTurn("work", "prover", state) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Pi never started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pe.SetModel(ctx, "provider", "missing"); err == nil || !strings.Contains(err.Error(), "Model not found") {
		t.Fatalf("invalid model: %v", err)
	}
	for _, level := range []string{"banana", "xhigh", "max"} {
		if _, err := pe.SetThinkingLevel(ctx, level); err == nil || !strings.Contains(err.Error(), "unsupported thinking") {
			t.Fatalf("invalid thinking %q: %v", level, err)
		}
	}
	settings, err := pe.SetModel(ctx, "provider", "second")
	if err != nil || settings != (PiSettings{"provider", "second", "low"}) {
		t.Fatalf("model readback: %+v, %v", settings, err)
	}
	settings, err = pe.SetThinkingLevel(ctx, "high")
	if err != nil || settings != (PiSettings{"provider", "second", "high"}) {
		t.Fatalf("thinking readback: %+v, %v", settings, err)
	}
	select {
	case result := <-resultCh:
		if result.Error != "" || result.Status != game.StatusConcede {
			t.Fatalf("result: status=%s error=%q log bytes=%d", result.Status, result.Error, len(result.Logs))
		}
	case <-ctx.Done():
		t.Fatal("Pi did not finish")
	}
	if pe.Model != "first" || pe.Thinking != "medium" {
		t.Fatal("invocation controls changed future defaults")
	}
}

func TestExecuteTurnRPCStopAndTimeout(t *testing.T) {
	for _, kind := range []string{"timeout", "stop"} {
		t.Run(kind, func(t *testing.T) {
			pe := helperExecutor(t, "hang")
			var stop atomic.Bool
			state := &game.TurnState{Dir: t.TempDir(), StopRequested: stop.Load, OnLiveEvent: func(game.LiveAgentEvent) {
				if kind == "stop" {
					stop.Store(true)
				}
			}}
			if kind == "timeout" {
				pe.Timeout = 1
			}
			result := pe.ExecuteTurn("work", "prover", state)
			want := "local_timeout:1"
			if kind == "stop" {
				want = "local_interrupted:stop_requested"
			}
			if result.Error != want || result.Recoverable {
				t.Fatalf("result: status=%s error=%q log bytes=%d", result.Status, result.Error, len(result.Logs))
			}
		})
	}
}

func helperExecutor(t *testing.T, scenario string) *PiExecutor {
	t.Helper()
	workspace := t.TempDir()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec " + shellQuote(executable) + " -test.run=^TestPiHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := platform.NewLocalPlatform(workspace)
	p.Env = map[string]string{"HOME": home, "TELOS_PI_HELPER": scenario, "GORACE": "atexit_sleep_ms=0"}
	return NewPiExecutor(p, "first", "medium", 10)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

// The test binary doubles as an isolated Pi process, exercising real OS pipes,
// argv, cancellation, and process exit without a model server or API credentials.
func TestPiHelperProcess(t *testing.T) {
	scenario := os.Getenv("TELOS_PI_HELPER")
	if scenario == "" {
		return
	}
	if os.Args[len(os.Args)-1] == "--version" {
		if version := os.Getenv("TELOS_PI_HELPER_VERSION"); version != "" {
			if version == "error" {
				os.Exit(1)
			}
			fmt.Println(version)
		} else if scenario == "legacy_version" {
			fmt.Println("0.84.2")
		} else {
			fmt.Println(piLiveSettingsVersion)
		}
		os.Exit(0)
	}
	args, _ := json.Marshal(os.Args)
	_ = os.WriteFile("args.json", args, 0o600)
	enc := json.NewEncoder(os.Stdout)
	emit := func(record interface{}) {
		if enc.Encode(record) != nil {
			os.Exit(3)
		}
	}
	message := func(text, reason, errorMessage string) {
		emit(map[string]interface{}{"type": "message_end", "message": map[string]interface{}{"role": "assistant", "model": "second", "content": []interface{}{map[string]interface{}{"type": "text", "text": text}}, "stopReason": reason, "errorMessage": errorMessage}})
	}
	finish := func() {
		message(strings.Repeat("x", 70000)+"\n<status>CONCEDE</status>", "stop", "")
		emit(map[string]string{"type": "agent_end"})
		emit(map[string]string{"type": "agent_settled"})
	}
	model, thinking := "first", "medium"
	dec := json.NewDecoder(os.Stdin)
	for {
		var request map[string]interface{}
		if err := dec.Decode(&request); err != nil {
			if err == io.EOF {
				if strings.HasSuffix(scenario, "_hang_exit") {
					time.Sleep(time.Minute)
				}
				os.Exit(0)
			}
			os.Exit(2)
		}
		command := getString(request, "type")
		if command == "set_model" || command == "set_thinking_level" {
			_ = os.WriteFile(command+".received", []byte("received"), 0o600)
		}
		response := map[string]interface{}{"id": request["id"], "type": "response", "command": command, "success": true}
		switch command {
		case "prompt":
			data, _ := json.Marshal(request)
			_ = os.WriteFile("prompt.json", data, 0o600)
			response["data"] = map[string]string{"disposition": "started"}
			if scenario == "legacy_version" {
				// Pi 0.84.2 acknowledges prompts without disposition data.
				delete(response, "data")
			}
			if scenario == "handled" {
				response["data"] = map[string]string{"disposition": "handled"}
			}
			if scenario == "rejected" || scenario == "rejected_hang_exit" {
				response["success"], response["error"] = false, "403: inactive virtual key"
			}
			emit(response)
			switch scenario {
			case "handled", "rejected", "rejected_hang_exit":
			case "malformed":
				fmt.Println("bad stdout")
			case "malformed_then_success":
				fmt.Println("bad stdout")
				finish()
			case "stderr":
				fmt.Fprintln(os.Stderr, "EROFS: read-only file system")
				os.Exit(1)
			case "provider_error":
				message("partial <status>CONCEDE</status>", "error", "overloaded_error: exhausted")
				emit(map[string]string{"type": "agent_settled"})
			case "truncated":
				message("partial", "length", "")
				emit(map[string]string{"type": "agent_settled"})
			case "aborted":
				message("partial", "aborted", "")
				emit(map[string]string{"type": "agent_settled"})
			case "early_exit":
				message("<status>CONCEDE</status>", "stop", "")
				os.Exit(0)
			case "retry":
				message("failed", "error", "api_error: retrying")
				emit(map[string]string{"type": "agent_end"})
				time.Sleep(20 * time.Millisecond)
				finish()
			case "denied_controls":
				message("<progress_update>Running.</progress_update>", "stop", "")
				go func() {
					for {
						if _, err := os.Stat("release"); err == nil {
							finish()
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
				}()
			case "controls", "hang":
				message("<progress_update>Running.</progress_update>", "stop", "")
				emit(map[string]string{"type": "agent_end"})
			case "dialog":
				emit(map[string]interface{}{"type": "extension_ui_request", "id": "dialog", "method": "confirm"})
			default:
				finish()
			}
		case "get_available_models":
			response["data"] = map[string]interface{}{"models": []map[string]string{{"provider": "provider", "id": "first"}, {"provider": "provider", "id": "second"}}}
			emit(response)
		case "set_model":
			if request["modelId"] == "missing" {
				response["success"], response["error"] = false, "Model not found"
			} else {
				model, thinking = getString(request, "modelId"), "low"
			}
			emit(response)
		case "get_available_thinking_levels":
			response["data"] = map[string]interface{}{"levels": []string{"off", "low", "medium", "high"}}
			emit(response)
		case "set_thinking_level":
			thinking = getString(request, "level")
			emit(response)
		case "get_state":
			response["data"] = map[string]interface{}{"model": map[string]string{"provider": "provider", "id": model}, "thinkingLevel": thinking}
			emit(response)
			if thinking == "high" {
				finish()
			}
		case "extension_ui_response":
			if request["id"] != "dialog" || request["cancelled"] != true {
				os.Exit(4)
			}
			finish()
		}
	}
}

func TestExecuteTurnRPCShutdownDeadlinePreservesCause(t *testing.T) {
	for _, tc := range []struct{ scenario, wantError string }{
		{"normal_hang_exit", "pi_rpc_shutdown_timeout"},
		{"rejected_hang_exit", "pi prompt: 403: inactive virtual key"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			pe := helperExecutor(t, tc.scenario)
			result := pe.ExecuteTurn("work", "prover", nil)
			if result.Error != tc.wantError {
				t.Fatalf("error: got %q want %q", result.Error, tc.wantError)
			}
		})
	}
}

func TestExecuteTurnRPCProgressCanConfigureAndFinishesBeforeReturn(t *testing.T) {
	pe := helperExecutor(t, "controls")
	callbackDone := make(chan error, 1)
	state := &game.TurnState{Dir: t.TempDir(), OnLiveEvent: func(game.LiveAgentEvent) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := pe.SetThinkingLevel(ctx, "high")
		// Keep the callback alive past process exit to exercise draining.
		time.Sleep(100 * time.Millisecond)
		callbackDone <- err
	}}
	result := pe.ExecuteTurn("work", "prover", state)
	if result.Error != "" {
		t.Fatal(result.Error)
	}
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatalf("callback RPC deadlocked: %v", err)
		}
	default:
		t.Fatal("ExecuteTurn returned before its progress callback")
	}
}

func TestExecuteTurnRPCLegacyVersionAcceptsLargePrompt(t *testing.T) {
	pe := helperExecutor(t, "legacy_version")
	result := pe.ExecuteTurn(strings.Repeat("x", 1<<20), "prover", nil)
	if result.Error != "" || result.Status != game.StatusConcede {
		t.Fatalf("ordinary legacy run failed: %q, status=%s", result.Error, result.Status)
	}
}

func TestExecuteTurnRPCUnverifiedVersionsRunWithoutLiveChanges(t *testing.T) {
	for _, version := range []string{"0.84.2", "1.0.5", "unknown\nversion \"banner\"", "error"} {
		t.Run(version, func(t *testing.T) {
			pe := helperExecutor(t, "denied_controls")
			pe.Platform.Env["TELOS_PI_HELPER_VERSION"] = version
			results := make(chan error, 2)
			state := &game.TurnState{Dir: t.TempDir(), OnLiveEvent: func(game.LiveAgentEvent) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, modelErr := pe.SetModel(ctx, "provider", "second")
				results <- modelErr
				_, thinkingErr := pe.SetThinkingLevel(ctx, "high")
				results <- thinkingErr
				_ = os.WriteFile(filepath.Join(pe.Platform.Workspace, "release"), []byte("go"), 0o600)
			}}
			result := pe.ExecuteTurn("work", "prover", state)
			if result.Error != "" || result.Status != game.StatusConcede {
				t.Fatalf("ordinary run was rejected: %q, status=%s", result.Error, result.Status)
			}
			for range 2 {
				if err := <-results; !errors.Is(err, ErrPiLiveSettingsUnsupported) {
					t.Fatalf("control should be unsupported: %v", err)
				}
			}
			for _, command := range []string{"set_model", "set_thinking_level"} {
				if _, err := os.Stat(filepath.Join(pe.Platform.Workspace, command+".received")); !os.IsNotExist(err) {
					t.Fatalf("unsupported mutation reached Pi: %s", command)
				}
			}
		})
	}
}
