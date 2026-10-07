package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPiRPCCorrelatesOutOfOrderResponses(t *testing.T) {
	rpc := newPiRPC()
	reader, writer := io.Pipe()
	rpc.start(writer)
	defer rpc.close(ErrPiNotRunning)
	defer reader.Close()
	done := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, command := range []string{"one", "two"} {
		go func() {
			data, err := rpc.call(ctx, command, nil)
			if err == nil && string(data) != `"`+command+`"` {
				err = errors.New("response assigned to wrong command")
			}
			done <- err
		}()
	}
	dec := json.NewDecoder(reader)
	var requests [2]map[string]interface{}
	for i := range requests {
		if err := dec.Decode(&requests[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := len(requests) - 1; i >= 0; i-- {
		r := requests[i]
		line, _ := json.Marshal(map[string]interface{}{"id": r["id"], "success": true, "data": r["type"]})
		rpc.respond(string(line))
	}
	for range requests {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPiRPCCancellationAndExitUnblockCommands(t *testing.T) {
	for _, phase := range []string{"startup", "write", "response", "exit"} {
		t.Run(phase, func(t *testing.T) {
			rpc := newPiRPC()
			defer rpc.close(ErrPiNotRunning)
			reader, writer := io.Pipe()
			defer reader.Close()
			if phase != "startup" {
				rpc.start(writer)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if phase == "response" || phase == "exit" {
				go func() {
					_, _ = bufio.NewReader(reader).ReadString('\n')
					if phase == "exit" {
						rpc.close(ErrPiNotRunning)
					}
				}()
			}
			_, err := rpc.call(ctx, "prompt", map[string]interface{}{"message": strings.Repeat("x", 100000)})
			want := error(context.DeadlineExceeded)
			if phase == "exit" {
				want = ErrPiNotRunning
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			rpc.mu.Lock()
			pending := len(rpc.pending)
			rpc.mu.Unlock()
			if pending != 0 {
				t.Fatal("leaked pending command")
			}
		})
	}
}

// Delivers a reply and closes the transport before Write itself returns.
// This reproduces a child that settles faster than the writer goroutine wakes.
type immediateReplyWriter struct{ rpc *piRPC }

func (w immediateReplyWriter) Write(data []byte) (int, error) {
	var request map[string]interface{}
	if err := json.Unmarshal(data, &request); err != nil {
		return 0, err
	}
	reply, _ := json.Marshal(map[string]interface{}{"id": request["id"], "success": true, "data": "accepted"})
	w.rpc.respond(string(reply))
	w.rpc.close(ErrPiNotRunning)
	return len(data), nil
}
func (w immediateReplyWriter) Close() error { return nil }

func TestPiRPCPrefersReplyBeforeTransportClosed(t *testing.T) {
	for range 100 {
		rpc := newPiRPC()
		rpc.start(immediateReplyWriter{rpc})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		data, err := rpc.call(ctx, "get_state", nil)
		cancel()
		if err != nil || string(data) != `"accepted"` {
			t.Fatalf("lost completed reply: %s, %v", data, err)
		}
	}
}

type settingsReplyWriter struct {
	rpc   *piRPC
	reply func(map[string]any) map[string]any
}

func (w settingsReplyWriter) Write(data []byte) (int, error) {
	var request map[string]any
	if err := json.Unmarshal(data, &request); err != nil {
		return 0, err
	}
	if response := w.reply(request); response != nil {
		response["id"], response["success"] = request["id"], true
		line, _ := json.Marshal(response)
		w.rpc.respond(string(line))
	}
	return len(data), nil
}

func (w settingsReplyWriter) Close() error { return nil }

func TestPiCombinedSettingsOutcomes(t *testing.T) {
	for _, phase := range []string{"applied", "partial", "missing-model", "thinking-timeout", "bad-readback", "partial-bad-readback", "partial-wrong-model"} {
		t.Run(phase, func(t *testing.T) {
			rpc := newPiRPC()
			rpc.setLiveSettingsSupport(true)
			defer rpc.close(ErrPiNotRunning)
			pe := &PiExecutor{active: rpc}
			var mu sync.Mutex
			var commands []string
			model, thinking := "old", "medium"
			rpc.start(settingsReplyWriter{rpc, func(request map[string]any) map[string]any {
				mu.Lock()
				defer mu.Unlock()
				command := request["type"].(string)
				commands = append(commands, command)
				var data any
				switch command {
				case "get_available_models":
					data = map[string]any{"models": []map[string]string{{"provider": "p", "id": "new"}}}
				case "set_model":
					model, thinking = "new", "low"
				case "get_available_thinking_levels":
					data = map[string]any{"levels": []string{"low", "high"}}
				case "set_thinking_level":
					thinking = request["level"].(string)
					if phase == "thinking-timeout" {
						return nil // The mutation happened, but its reply was lost.
					}
				case "get_state":
					if phase == "partial-wrong-model" {
						model = "unexpected"
					}
					data = map[string]any{"model": map[string]string{"provider": "p", "id": model}, "thinkingLevel": thinking}
					if strings.HasSuffix(phase, "bad-readback") {
						data = map[string]any{}
					}
				}
				return map[string]any{"data": data}
			}})
			update := PiSettingsUpdate{Provider: "p", Model: "new", Thinking: "high"}
			if strings.HasPrefix(phase, "partial") {
				update.Thinking = "banana"
			}
			if phase == "missing-model" {
				update.Model = "missing"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			settings, err := pe.SetSettings(ctx, update)
			mu.Lock()
			gotCommands := append([]string(nil), commands...)
			mu.Unlock()
			switch phase {
			case "applied":
				if err != nil || settings != (PiSettings{"p", "new", "high"}) || !reflect.DeepEqual(gotCommands, []string{"get_available_models", "set_model", "get_available_thinking_levels", "set_thinking_level", "get_state"}) {
					t.Fatalf("combined: %+v %v %v", settings, err, gotCommands)
				}
			case "partial":
				if !errors.Is(err, ErrPiSettingsPartiallyApplied) || errors.Is(err, ErrPiSettingsRejected) || settings != (PiSettings{"p", "new", "low"}) || !reflect.DeepEqual(gotCommands, []string{"get_available_models", "set_model", "get_available_thinking_levels", "get_state"}) {
					t.Fatalf("partial: %+v %v %v", settings, err, gotCommands)
				}
			case "missing-model":
				if !errors.Is(err, ErrPiSettingsRejected) || !reflect.DeepEqual(gotCommands, []string{"get_available_models"}) {
					t.Fatalf("model rejection sent another command: %v %v", err, gotCommands)
				}
			default:
				if err == nil || errors.Is(err, ErrPiSettingsPartiallyApplied) || errors.Is(err, ErrPiSettingsRejected) || settings != (PiSettings{}) {
					t.Fatalf("uncertain outcome: %+v %v", settings, err)
				}
			}
		})
	}
}
