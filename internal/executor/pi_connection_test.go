package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/sessionapi"
)

// Real HTTP adapters must use the selected account's proxy while bash tools
// continue to use the deployment's general proxy. Run two Pi invocations at
// once to catch accidental process-global or shared-file routing changes.
func TestPiConnectionsRouteModelsAndToolsSeparately(t *testing.T) {
	binary := os.Getenv("TELOS_TEST_PI_BINARY")
	if binary == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI must provide TELOS_TEST_PI_BINARY for native Pi routing tests")
		}
		t.Skip("set TELOS_TEST_PI_BINARY to exercise native Pi HTTP transport")
	}
	for _, api := range []string{"openai-completions", "openai-responses", "anthropic-messages", "openai-codex-responses"} {
		t.Run(api, func(t *testing.T) { testPiConnectionTransport(t, binary, api) })
	}
}

func testPiConnectionTransport(t *testing.T, binary, api string) {
	provider, path := "telos-bifrost", "/v1/chat/completions"
	baseURL := "https://models.test/v1"
	switch api {
	case "openai-responses":
		provider, path = "openai", "/v1/responses"
	case "anthropic-messages":
		provider, path = "anthropic", "/v1/messages"
		baseURL = "https://models.test"
	case "openai-codex-responses":
		provider, path = "openai-codex", "/v1/codex/responses"
	}
	root := t.TempDir()
	certificate, ca := piConnectionTestCertificate(t)
	caPath := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caPath, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	var toolRequests atomic.Int32
	general := piConnectionTestProxy(t, certificate, func(r *http.Request) (string, string) {
		if r.Host != "tools.test" || r.URL.Path != "/check" {
			t.Errorf("model request escaped to general proxy: %s%s", r.Host, r.URL.Path)
		}
		toolRequests.Add(1)
		return "text/plain", "general-tool-output"
	})
	defer general.Close()
	type invocation struct {
		command   *exec.Cmd
		output    bytes.Buffer
		calls     atomic.Int32
		key       string
		profileID string
		receipt   string
		modelID   string
	}
	runs := make([]*invocation, 0, 2)
	for i := 0; i < 2; i++ {
		run := &invocation{key: "telos-proxy-" + strings.Repeat(string(rune('a'+i)), 43), profileID: fmt.Sprintf("profile-%d", i), modelID: "probe"}
		if api == "openai-codex-responses" {
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"telos-proxy"}}`))
			run.key = "telos-proxy." + claims + "." + strings.Repeat(string(rune('a'+i)), 43)
		}
		if i == 1 {
			run.modelID = "configured-child-model"
		}
		checkHeaders := func(r *http.Request) {
			if r.Host != "models.test" || r.URL.Path != path {
				t.Errorf("tool request escaped to model proxy: %s%s", r.Host, r.URL.Path)
			}
			if api == "anthropic-messages" {
				if r.Header.Get("x-api-key") != run.key {
					t.Error("Anthropic request used the wrong account credential")
				}
			} else if r.Header.Get("Authorization") != "Bearer "+run.key {
				t.Error("model request used the wrong account credential")
			}
			if provider == "telos-bifrost" && r.Header.Get("x-bf-vk") != run.key {
				t.Error("managed request used the wrong virtual key")
			}
			if api == "openai-codex-responses" && r.Header.Get("chatgpt-account-id") != "telos-proxy" {
				t.Error("Codex request used the wrong account ID")
			}
			for name, values := range r.Header {
				for _, value := range values {
					if strings.Contains(value, "stale-") {
						t.Errorf("old connection header reached new account: %s", name)
					}
				}
			}
		}
		checkBody := func(body []byte) int32 {
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil || payload["model"] != run.modelID {
				t.Errorf("selected model metadata lost: %s, %v", body, err)
			}
			if api == "openai-completions" && run.modelID == "configured-child-model" && payload["max_tokens"] != float64(3072) {
				t.Errorf("configured model override lost: %s", body)
			}
			call := run.calls.Add(1)
			if call > 1 && !bytes.Contains(body, []byte("general-tool-output")) {
				t.Error("model follow-up lost the tool result")
			}
			return call
		}
		var proxy *httptest.Server
		if api == "openai-codex-responses" {
			proxy = piConnectionTestTunnel(t, certificate, func(connection net.Conn, reader *bufio.Reader, r *http.Request) {
				checkHeaders(r)
				if r.Method != "GET" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					t.Error("Codex must use its WebSocket transport; SSE fallback is not coverage")
					return
				}
				accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				_, _ = fmt.Fprintf(connection, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(accept[:]))
				for {
					opcode, body, err := piConnectionReadFrame(reader)
					if err != nil || opcode == 8 {
						return
					}
					if opcode == 9 {
						_ = piConnectionWriteFrame(connection, 10, body)
						continue
					}
					if opcode != 1 {
						t.Errorf("unexpected WebSocket opcode %d", opcode)
						return
					}
					call := checkBody(body)
					for _, event := range piConnectionResponseEvents(call == 1) {
						data, _ := json.Marshal(event)
						if err := piConnectionWriteFrame(connection, 1, data); err != nil {
							t.Error(err)
							return
						}
					}
				}
			})
		} else {
			proxy = piConnectionTestProxy(t, certificate, func(r *http.Request) (string, string) {
				checkHeaders(r)
				body, _ := io.ReadAll(r.Body)
				call := checkBody(body)
				return "text/event-stream", piConnectionStream(api, run.modelID, call == 1)
			})
		}
		defer proxy.Close()
		dir := filepath.Join(root, run.profileID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		write := func(name string, data []byte) string {
			t.Helper()
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}
		transport := "sse"
		if api == "openai-codex-responses" {
			transport = "websocket"
		}
		write("settings.json", []byte(fmt.Sprintf(`{"compaction":{"enabled":false},"retry":{"enabled":false},"transport":%q}`, transport)))
		extension := write("startup.js", piStartupExtension)
		run.receipt = filepath.Join(dir, "receipt.json")
		definition := json.RawMessage(fmt.Sprintf(`{"id":%q,"name":"Probe","api":%q,"reasoning":false,"input":["text"],"contextWindow":128000,"maxTokens":4096,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`, run.modelID, api))
		var configured map[string]any
		if err := json.Unmarshal(definition, &configured); err != nil {
			t.Fatal(err)
		}
		// Provider, model, and model-override headers all survive Pi's provider
		// composition. None may override a different connection's credentials.
		staleHeaders := func(level string) map[string]string {
			return map[string]string{"Authorization": "Bearer stale-" + level, "x-api-key": "stale-" + level, "chatgpt-account-id": "stale-" + level, "OpenAI-Organization": "stale-" + level, "x-bf-vk": "stale-" + level}
		}
		configured["baseUrl"] = "https://old-model.test/v1"
		configured["headers"] = staleHeaders("model")
		override := map[string]any{"headers": staleHeaders("override")}
		if i == 1 {
			// An explicitly selected child model inherits the connection but
			// obtains its definition and overrides from the configured registry.
			definition = nil
			override["maxTokens"] = 3072
			if api == "openai-completions" {
				override["compat"] = map[string]string{"maxTokensField": "max_tokens"}
			}
		}
		modelsData, _ := json.Marshal(map[string]any{"providers": map[string]any{provider: map[string]any{
			"baseUrl": "https://old.test/v1", "api": api, "apiKey": "stale-key", "headers": staleHeaders("provider"),
			"models": []any{configured}, "modelOverrides": map[string]any{run.modelID: override},
		}}})
		write("models.json", modelsData)
		config := PiStartupConfig{RequestID: "switch", AttemptID: run.profileID, Model: provider + "/" + run.modelID, Thinking: "off", Definition: definition, ReceiptPath: run.receipt,
			Connection: &sessionapi.InferenceConnection{ID: run.profileID, Provider: provider, BaseURL: baseURL, ProxyURL: proxy.URL, APIKey: run.key, AuthHeader: i == 1 && api != "anthropic-messages"}}
		// The extension is invoked directly to use local test proxy ports. The
		// operator API separately tests the production address/port allowlist.
		data, _ := json.Marshal(config)
		configPath := write("startup.json", data)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		run.command = exec.CommandContext(ctx, binary, "--offline", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--tools", "bash", "-e", extension, "--model", config.Model, "--thinking", config.Thinking, "--mode", "text", "--print", "Run the tool, then report success.")
		run.command.Env = append(os.Environ(), "HOME="+dir, "PI_CODING_AGENT_DIR="+dir, "PI_TELEMETRY=0", "TELOS_PI_STARTUP_CONFIG="+configPath,
			"OPENAI_ORG_ID=stale-sdk-org", "OPENAI_PROJECT_ID=stale-sdk-project",
			"HTTP_PROXY="+general.URL, "HTTPS_PROXY="+general.URL, "http_proxy="+general.URL, "https_proxy="+general.URL, "OPENCLAW_PROXY_URL="+general.URL, "NO_PROXY=", "no_proxy=", "NODE_EXTRA_CA_CERTS="+caPath, "SSL_CERT_FILE="+caPath, "CURL_CA_BUNDLE="+caPath)
		run.command.Stdout, run.command.Stderr = &run.output, &run.output
		if err := run.command.Start(); err != nil {
			t.Fatal(err)
		}
		defer run.command.Process.Kill()
		runs = append(runs, run)
	}
	for _, run := range runs {
		if err := run.command.Wait(); err != nil || !strings.Contains(run.output.String(), "<status>CONCEDE</status>") {
			t.Fatalf("Pi routing failed: %v\n%s", err, run.output.String())
		}
		if run.calls.Load() != 2 {
			t.Fatalf("model requests = %d, want 2", run.calls.Load())
		}
		receipt, err := ReadPiStartupReceipt(run.receipt)
		if err != nil || receipt.Error != "" || receipt.ConnectionID != run.profileID {
			t.Fatalf("connection not confirmed: %+v %v", receipt, err)
		}
	}
	if toolRequests.Load() != 2 {
		t.Fatalf("tool requests = %d, want 2", toolRequests.Load())
	}
}

func piConnectionCompletion(delta map[string]any, finish string) string {
	chunk := map[string]any{"id": "completion", "object": "chat.completion.chunk", "created": 1, "model": "probe", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}
	data, _ := json.Marshal(chunk)
	chunk["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}
	done, _ := json.Marshal(chunk)
	return "data: " + string(data) + "\n\ndata: " + string(done) + "\n\ndata: [DONE]\n\n"
}

const piConnectionToolArguments = `{"command":"test \"$OPENAI_ORG_ID\" = stale-sdk-org && test \"$OPENAI_PROJECT_ID\" = stale-sdk-project && curl --fail --silent https://tools.test/check"}`
const piConnectionFinalText = "Complete.\n<status>CONCEDE</status>"

func piConnectionResponseEvents(tool bool) []map[string]any {
	item := map[string]any{"type": "message", "id": "msg_final", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": piConnectionFinalText, "annotations": []any{}}}}
	id := "resp_final"
	if tool {
		id = "resp_tool"
		item = map[string]any{"type": "function_call", "id": "fc_tool", "call_id": "call_tool", "name": "bash", "arguments": piConnectionToolArguments, "status": "completed"}
	}
	response := map[string]any{"id": id, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
	}
	if !tool {
		events = append(events, map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": piConnectionFinalText})
	}
	return append(events,
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
		map[string]any{"type": "response.completed", "response": response},
	)
}

func piConnectionStream(api, model string, tool bool) string {
	if api == "openai-completions" {
		if tool {
			return piConnectionCompletion(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_tool", "type": "function", "function": map[string]any{"name": "bash", "arguments": piConnectionToolArguments}}}}, "tool_calls")
		}
		return piConnectionCompletion(map[string]any{"role": "assistant", "content": piConnectionFinalText}, "stop")
	}
	events := piConnectionResponseEvents(tool)
	if api == "anthropic-messages" {
		block := map[string]any{"type": "text", "text": ""}
		delta := map[string]any{"type": "text_delta", "text": piConnectionFinalText}
		reason := "end_turn"
		if tool {
			block = map[string]any{"type": "tool_use", "id": "call_tool", "name": "bash", "input": map[string]any{}}
			delta = map[string]any{"type": "input_json_delta", "partial_json": piConnectionToolArguments}
			reason = "tool_use"
		}
		events = []map[string]any{
			{"type": "message_start", "message": map[string]any{"id": "msg_probe", "type": "message", "role": "assistant", "model": model, "content": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}},
			{"type": "content_block_start", "index": 0, "content_block": block},
			{"type": "content_block_delta", "index": 0, "delta": delta},
			{"type": "content_block_stop", "index": 0},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}},
			{"type": "message_stop"},
		}
	}
	var stream strings.Builder
	for _, event := range events {
		data, _ := json.Marshal(event)
		fmt.Fprintf(&stream, "event: %s\ndata: %s\n\n", event["type"], data)
	}
	return stream.String()
}

// The test server only needs complete masked client frames and unmasked replies.
// Keep the protocol fixture local instead of adding a runtime WebSocket dependency.
func piConnectionReadFrame(reader io.Reader) (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	if header[0]&0x80 == 0 || header[1]&0x80 == 0 {
		return 0, nil, fmt.Errorf("expected complete masked WebSocket frame")
	}
	size := uint64(header[1] & 0x7f)
	if size == 126 {
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(extended[:]))
	} else if size == 127 {
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(extended[:])
	}
	if size > 1<<20 {
		return 0, nil, fmt.Errorf("unexpected WebSocket payload length %d", size)
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return 0, nil, err
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(reader, data); err != nil {
		return 0, nil, err
	}
	for i := range data {
		data[i] ^= mask[i%4]
	}
	return header[0] & 0x0f, data, nil
}

func piConnectionWriteFrame(writer io.Writer, opcode byte, data []byte) error {
	var frame bytes.Buffer
	frame.WriteByte(0x80 | opcode)
	if len(data) < 126 {
		frame.WriteByte(byte(len(data)))
	} else {
		frame.WriteByte(127)
		_ = binary.Write(&frame, binary.BigEndian, uint64(len(data)))
	}
	frame.Write(data)
	_, err := writer.Write(frame.Bytes())
	return err
}

func piConnectionTestProxy(t *testing.T, certificate tls.Certificate, respond func(*http.Request) (string, string)) *httptest.Server {
	t.Helper()
	return piConnectionTestTunnel(t, certificate, func(connection net.Conn, _ *bufio.Reader, request *http.Request) {
		kind, body := respond(request)
		response := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": {kind}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Close: true}
		if err := response.Write(connection); err != nil {
			t.Error(err)
		}
	})
}

func piConnectionTestTunnel(t *testing.T, certificate tls.Certificate, respond func(net.Conn, *bufio.Reader, *http.Request)) *httptest.Server {
	t.Helper()
	var connections sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("expected CONNECT, got %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		connection, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		connections.Add(1)
		defer connections.Done()
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		secure := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
		defer secure.Close()
		reader := bufio.NewReader(secure)
		request, err := http.ReadRequest(reader)
		if err != nil {
			t.Error(err)
			return
		}
		defer request.Body.Close()
		respond(secure, reader, request)
	}))
	t.Cleanup(func() { server.Close(); connections.Wait() })
	return server
}

func piConnectionTestCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Pi connection test"}, DNSNames: []string{"models.test", "tools.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
