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
		if response["id"] == nil {
			response["id"] = request["id"]
		}
		response["success"] = true
		line, _ := json.Marshal(response)
		w.rpc.respond(string(line))
	}
	return len(data), nil
}

func (w settingsReplyWriter) Close() error { return nil }

func TestPiCombinedSettingsOutcomes(t *testing.T) {
	for _, phase := range []string{"applied", "rejected", "missing-extension", "prepare-timeout", "commit-timeout", "bad-preparation", "bad-readback", "unowned-readback", "wrong-thinking"} {
		t.Run(phase, func(t *testing.T) {
			rpc := newPiRPC()
			rpc.setLiveSettingsSupport(true)
			defer rpc.close(ErrPiNotRunning)
			pe := &PiExecutor{active: rpc}
			var mu sync.Mutex
			var commands []string
			model, thinking := "old", "medium"
			const preparedID = "telos-internal-settings-test"
			rpc.start(settingsReplyWriter{rpc, func(request map[string]any) map[string]any {
				mu.Lock()
				defer mu.Unlock()
				command := request["type"].(string)
				commands = append(commands, command)
				var data any
				switch command {
				case "get_commands":
					data = map[string]any{"commands": []map[string]string{{"name": "telos-internal-prepare-settings"}}}
					if phase == "missing-extension" {
						data = map[string]any{"commands": []any{}}
					}
				case "prompt":
					var prepared map[string]string
					_, body, _ := strings.Cut(request["message"].(string), " ")
					if err := json.Unmarshal([]byte(body), &prepared); err != nil {
						t.Error(err)
					}
					if phase == "prepare-timeout" {
						return nil
					}
					data = map[string]string{"provider": "p", "model": "new", "thinking": "high", "modelId": preparedID}
					if phase == "rejected" {
						data = map[string]string{"error": "Unsupported thinking level"}
					}
					if phase == "bad-preparation" {
						data = map[string]string{"provider": "p", "model": "different", "thinking": "high", "modelId": preparedID}
					}
					// A successful native prompt ACK must not mask rejection or loss
					// of the extension's separate preparation result.
					ack, _ := json.Marshal(map[string]any{"id": request["id"], "success": true})
					rpc.respond(string(ack))
					return map[string]any{"id": prepared["id"], "data": data}
				case "set_model":
					model, thinking = request["modelId"].(string), "high"
					if phase == "commit-timeout" {
						return nil // The entire pair changed, but its reply was lost.
					}
				case "get_state":
					if phase == "unowned-readback" {
						model = "telos-internal-settings-unowned"
					}
					if phase == "wrong-thinking" {
						thinking = "low"
					}
					data = map[string]any{"model": map[string]string{"provider": "p", "id": model, "api": "pi-virtual"}, "thinkingLevel": thinking}
					if phase == "bad-readback" {
						data = map[string]any{}
					}
				default:
					t.Errorf("unexpected separate settings command: %s", command)
				}
				return map[string]any{"data": data}
			}})
			update := PiSettingsUpdate{Provider: "p", Model: "new", Thinking: "high"}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			settings, err := pe.SetSettings(ctx, update)
			mu.Lock()
			gotCommands := append([]string(nil), commands...)
			mu.Unlock()
			switch phase {
			case "applied":
				if err != nil || settings != (PiSettings{"p", "new", "high"}) || !reflect.DeepEqual(gotCommands, []string{"get_commands", "prompt", "set_model", "get_state"}) {
					t.Fatalf("combined: %+v %v %v", settings, err, gotCommands)
				}
			case "rejected", "missing-extension":
				if !errors.Is(err, ErrPiSettingsRejected) || model != "old" || thinking != "medium" {
					t.Fatalf("rejection changed settings: %+v %s %s %v %v", settings, model, thinking, err, gotCommands)
				}
			default:
				if err == nil || errors.Is(err, ErrPiSettingsRejected) || settings != (PiSettings{}) {
					t.Fatalf("uncertain outcome: %+v %v", settings, err)
				}
			}
			if phase == "commit-timeout" && (model != preparedID || thinking != "high") {
				t.Fatalf("lost reply split the pair: %s %s", model, thinking)
			}
		})
	}
}
