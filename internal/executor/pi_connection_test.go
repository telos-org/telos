package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
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
		t.Skip("set TELOS_TEST_PI_BINARY to exercise native Pi HTTP transport")
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
		if i == 1 {
			run.modelID = "configured-child-model"
		}
		proxy := piConnectionTestProxy(t, certificate, func(r *http.Request) (string, string) {
			if r.Host != "models.test" || r.URL.Path != "/v1/chat/completions" {
				t.Errorf("tool request escaped to model proxy: %s%s", r.Host, r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer "+run.key || r.Header.Get("x-bf-vk") != run.key {
				t.Error("model request used the wrong account credential")
			}
			for name, values := range r.Header {
				for _, value := range values {
					if strings.Contains(value, "stale-") {
						t.Errorf("old connection header reached new account: %s", name)
					}
				}
			}
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil || payload["model"] != run.modelID {
				t.Errorf("selected model metadata lost: %s, %v", body, err)
			}
			if run.modelID == "configured-child-model" && payload["max_tokens"] != float64(3072) {
				t.Errorf("configured model override lost: %s", body)
			}
			call := run.calls.Add(1)
			if call == 1 {
				return "text/event-stream", piConnectionCompletion(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_tool", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"test \"$OPENAI_ORG_ID\" = stale-sdk-org && test \"$OPENAI_PROJECT_ID\" = stale-sdk-project && curl --fail --silent https://tools.test/check"}`}}}}, "tool_calls")
			}
			if !bytes.Contains(body, []byte("general-tool-output")) {
				t.Error("model follow-up lost the tool result")
			}
			return "text/event-stream", piConnectionCompletion(map[string]any{"role": "assistant", "content": "Complete.\n<status>CONCEDE</status>"}, "stop")
		})
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
		write("settings.json", []byte(`{"compaction":{"enabled":false},"retry":{"enabled":false},"transport":"sse"}`))
		extension := write("startup.js", piStartupExtension)
		run.receipt = filepath.Join(dir, "receipt.json")
		definition := json.RawMessage(fmt.Sprintf(`{"id":%q,"name":"Probe","api":"openai-completions","reasoning":false,"input":["text"],"contextWindow":128000,"maxTokens":4096,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`, run.modelID))
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
			override["compat"] = map[string]string{"maxTokensField": "max_tokens"}
		}
		modelsData, _ := json.Marshal(map[string]any{"providers": map[string]any{"telos-bifrost": map[string]any{
			"baseUrl": "https://old.test/v1", "api": "openai-completions", "apiKey": "stale-key", "headers": staleHeaders("provider"),
			"models": []any{configured}, "modelOverrides": map[string]any{run.modelID: override},
		}}})
		write("models.json", modelsData)
		config := PiStartupConfig{RequestID: "switch", AttemptID: run.profileID, Model: "telos-bifrost/" + run.modelID, Thinking: "off", Definition: definition, ReceiptPath: run.receipt,
			Connection: &sessionapi.InferenceConnection{ID: run.profileID, Provider: "telos-bifrost", BaseURL: "https://models.test/v1", ProxyURL: proxy.URL, APIKey: run.key, AuthHeader: i == 1}}
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

func piConnectionTestProxy(t *testing.T, certificate tls.Certificate, respond func(*http.Request) (string, string)) *httptest.Server {
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
		request, err := http.ReadRequest(bufio.NewReader(secure))
		if err != nil {
			t.Error(err)
			return
		}
		defer request.Body.Close()
		kind, body := respond(request)
		response := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": {kind}}, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Close: true}
		if err := response.Write(secure); err != nil {
			t.Error(err)
		}
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
