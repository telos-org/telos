package cloud

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

type readRetryTransport func(*http.Request) (*http.Response, error)

func (transport readRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestMonitoringReadRetriesOnlyTransientErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		attempts int
	}{
		{"reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, 3},
		{"connection_refused", syscall.ECONNREFUSED, 3},
		{"wrapped_connection_refused", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, 3},
		{"aborted", syscall.ECONNABORTED, 3},
		{"broken_pipe", syscall.EPIPE, 3},
		{"eof", io.EOF, 3},
		{"truncated_body", io.ErrUnexpectedEOF, 3},
		{"timeout", &net.DNSError{Err: "temporary timeout", IsTimeout: true}, 3},
		{"temporary_dns", &net.DNSError{Err: "server misbehaving", IsTemporary: true}, 3},
		{"wrapped_temporary_dns", &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("resolve endpoint: %w", &net.DNSError{Err: "server misbehaving", IsTemporary: true})}, 3},
		// Transport timeouts can match DeadlineExceeded before the operation's
		// deadline expires (for example, ResponseHeaderTimeout).
		{"transport_deadline", &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}, 3},
		{"canceled", context.Canceled, 1},
		{"unknown_host", &net.DNSError{Err: "no such host", IsNotFound: true}, 1},
		{"permanent_dns", &net.DNSError{Err: "invalid DNS response"}, 1},
		{"canceled_temporary_dns", &net.DNSError{Err: "lookup canceled", IsTemporary: true, UnwrapErr: context.Canceled}, 1},
		{"certificate", x509.UnknownAuthorityError{}, 1},
		{"invalid_json", &json.SyntaxError{}, 1},
		{"http2_protocol_error", errors.New("stream error: stream ID 1; PROTOCOL_ERROR; received from peer"), 1},
		{"http2_local_internal_error", errors.New("stream error: stream ID 1; INTERNAL_ERROR; local protocol failure"), 1},
		{"unexpected_error", errors.New("unsupported protocol"), 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				client := NewClient("https://api.example.test", "")
				client.HTTP.Transport = readRetryTransport(func(*http.Request) (*http.Response, error) {
					attempts++
					return nil, test.err
				})
				result, err := client.GetSession("session-test")
				if result != nil || !errors.Is(err, test.err) {
					t.Fatalf("result = %v, error = %v; want original error %v", result, err, test.err)
				}
				if attempts != test.attempts {
					t.Fatalf("attempts = %d, want %d", attempts, test.attempts)
				}
			})
		})
	}
}

func TestMonitoringReadRecoversOnLastAttempt(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"connection_reset", syscall.ECONNRESET},
		{"connection_refused", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}},
		{"temporary_dns", &net.DNSError{Err: "server misbehaving", IsTemporary: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var starts []time.Time
				client := NewClient("https://api.example.test", "")
				client.HTTP.Transport = readRetryTransport(func(*http.Request) (*http.Response, error) {
					starts = append(starts, time.Now())
					if len(starts) < 3 {
						return nil, test.err
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"id":"session-test","state":"running"}`)),
					}, nil
				})
				result, err := client.GetSession("session-test")
				if err != nil || result.ID != "session-test" || result.State != "running" {
					t.Fatalf("result = %v, error = %v", result, err)
				}
				if len(starts) != 3 {
					t.Fatalf("attempts = %d, want 3", len(starts))
				}
				for index, bounds := range [][2]time.Duration{
					{125 * time.Millisecond, 250 * time.Millisecond},
					{250 * time.Millisecond, 500 * time.Millisecond},
				} {
					wait := starts[index+1].Sub(starts[index])
					if wait < bounds[0] || wait >= bounds[1] {
						t.Errorf("wait %d = %v, want [%v, %v)", index+1, wait, bounds[0], bounds[1])
					}
				}
			})
		})
	}
}

func TestMonitoringReadSharesTimeoutAcrossAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := NewClient("https://api.example.test", "")
		client.HTTP.Timeout = time.Second
		attempts := 0
		client.HTTP.Transport = readRetryTransport(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts < 3 {
				time.Sleep(100 * time.Millisecond)
				return nil, syscall.ECONNRESET
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		start := time.Now()
		_, err := client.GetSession("session-test")
		if !errors.Is(err, context.DeadlineExceeded) || attempts != 3 {
			t.Fatalf("attempts = %d, error = %v", attempts, err)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("elapsed = %v, want one shared 1s budget", elapsed)
		}
	})
}

func TestMonitoringReadBoundsClientsWithoutHTTPTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := NewClient("https://api.example.test", "")
		client.HTTP.Timeout = 0
		attempts := 0
		client.HTTP.Transport = readRetryTransport(func(req *http.Request) (*http.Response, error) {
			attempts++
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		start := time.Now()
		_, err := client.GetSession("session-test")
		if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
			t.Fatalf("attempts = %d, error = %v", attempts, err)
		}
		if elapsed := time.Since(start); elapsed != 30*time.Second {
			t.Fatalf("elapsed = %v, want the default 30s budget", elapsed)
		}
	})
}

func TestMonitoringReadStopsDuringBackoff(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "deadline"
		if canceled {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := NewClient("https://api.example.test", "")
				client.HTTP.Timeout = 50 * time.Millisecond
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				wantErr := context.DeadlineExceeded
				if canceled {
					client.HTTP.Timeout = time.Second
					wantErr = context.Canceled
					go func() {
						time.Sleep(50 * time.Millisecond)
						cancel()
					}()
				}
				attempts := 0
				client.HTTP.Transport = readRetryTransport(func(*http.Request) (*http.Response, error) {
					attempts++
					return nil, syscall.ECONNRESET
				})
				start := time.Now()
				_, err := getJSONWithRetry[SessionRecord](ctx, client, "/api/deployments/session-test")
				if !errors.Is(err, wantErr) || attempts != 1 {
					t.Fatalf("attempts = %d, error = %v; want %v", attempts, err, wantErr)
				}
				if elapsed := time.Since(start); elapsed != 50*time.Millisecond {
					t.Fatalf("elapsed = %v; wait should stop at 50ms", elapsed)
				}
			})
		})
	}
}

func TestMonitoringReadHonorsAlreadyCanceledContext(t *testing.T) {
	client := NewClient("https://api.example.test", "")
	client.HTTP.Transport = readRetryTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled read must not send a request")
		return nil, context.Canceled
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := getJSONWithRetry[SessionRecord](ctx, client, "/api/deployments/session-test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

type readRetryBody struct {
	io.Reader
	closed bool
}

func (body *readRetryBody) Close() error {
	body.closed = true
	return nil
}

type readRetryTruncatedBody struct {
	data string
}

func (body *readRetryTruncatedBody) Read(p []byte) (int, error) {
	n := copy(p, body.data)
	body.data = body.data[n:]
	if len(body.data) > 0 {
		return n, nil
	}
	return n, io.ErrUnexpectedEOF
}

type readRetryStalledBody struct {
	ctx context.Context
}

func (body readRetryStalledBody) Read([]byte) (int, error) {
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func TestMonitoringReadCompleteMalformedJSONIsTerminal(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "whitespace", body: " \n\t"},
		{name: "partial object", body: `{"id":"incomplete"`},
		{name: "trailing garbage", body: `{"id":"first"} garbage`},
		{name: "multiple JSON values", body: `{"id":"first"}{"id":"second"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				body := &readRetryBody{Reader: strings.NewReader(tt.body)}
				client := NewClient("https://api.example.test", "test-token")
				client.HTTP.Transport = readRetryTransport(func(req *http.Request) (*http.Response, error) {
					attempts++
					if attempts > 1 {
						return &http.Response{
							StatusCode: http.StatusOK,
							Body:       io.NopCloser(strings.NewReader(`{"id":"unexpected retry"}`)),
						}, nil
					}
					return &http.Response{
						StatusCode:    http.StatusOK,
						ContentLength: int64(len(tt.body)),
						Body:          body,
					}, nil
				})

				result, err := client.GetSession("session-test")
				var syntaxError *json.SyntaxError
				if result != nil || !errors.As(err, &syntaxError) {
					t.Fatalf("GetSession() = %v, %v; want a terminal JSON syntax error", result, err)
				}
				if attempts != 1 {
					t.Fatalf("attempts = %d, want 1 for a complete HTTP response with invalid JSON", attempts)
				}
				if !body.closed {
					t.Fatal("malformed response body was not closed")
				}
			})
		})
	}
}

func TestMonitoringReadCompleteJSONInTruncatedHTTPBodyRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		// A JSON decoder can accept this first object before noticing that the
		// HTTP response is incomplete. No fields from it may survive the retry.
		firstBody := &readRetryBody{Reader: &readRetryTruncatedBody{
			data: `{"id":"first","name":"discarded","failure_reason":"stale"}`,
		}}
		secondBody := &readRetryBody{Reader: strings.NewReader(`{"id":"second"}`)}
		client := NewClient("https://api.example.test", "test-token")
		client.HTTP.Transport = readRetryTransport(func(req *http.Request) (*http.Response, error) {
			attempts++
			body := secondBody
			if attempts == 1 {
				body = firstBody
			} else if !firstBody.closed {
				t.Error("first response body was not closed before retrying")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
		})

		result, err := client.GetSession("session-test")
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if result.ID != "second" || result.Name != "" || result.FailureReason != nil {
			t.Fatalf("GetSession() = %+v; want only fields from the complete second response", result)
		}
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
		if !firstBody.closed || !secondBody.closed {
			t.Fatal("response bodies were not both closed")
		}
	})
}

func TestMonitoringReadTerminalStatusSurvivesErrorBodyDeadline(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				var body *readRetryBody
				client := NewClient("https://api.example.test", "test-token")
				client.HTTP.Timeout = time.Second
				client.HTTP.Transport = readRetryTransport(func(req *http.Request) (*http.Response, error) {
					attempts++
					body = &readRetryBody{Reader: readRetryStalledBody{ctx: req.Context()}}
					return &http.Response{StatusCode: status, Body: body}, nil
				})

				start := time.Now()
				result, err := client.GetSession("session-test")
				if result != nil || !IsStatus(err, status) {
					t.Fatalf("GetSession() = %v, %v; want HTTP %d after error body timed out", result, err, status)
				}
				if attempts != 1 {
					t.Fatalf("attempts = %d, want 1 for a terminal HTTP status", attempts)
				}
				if elapsed := time.Since(start); elapsed != time.Second {
					t.Fatalf("elapsed = %v, want the one-second operation timeout", elapsed)
				}
				if !body.closed {
					t.Fatal("timed-out error response body was not closed")
				}
			})
		})
	}
}

func TestMonitoringReadResponseHeaderTimeoutRetries(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if attempts.Add(1) == 1 {
			<-req.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"id":"session-test"}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "test-token")
	client.HTTP.Timeout = 5 * time.Second
	transport := &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 100 * time.Millisecond,
	}
	defer transport.CloseIdleConnections()
	client.HTTP.Transport = transport

	result, err := client.GetSession("session-test")
	if err != nil || result == nil || result.ID != "session-test" {
		t.Fatalf("GetSession() = %v, %v; want recovery from a response-header timeout", result, err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestNetworkCloudHTTP2StreamReset(t *testing.T) {
	for _, test := range []struct {
		name          string
		body          string
		beforeHeaders bool
		failures      int32
		status        int
		wantAttempts  int32
	}{
		{name: "before headers", beforeHeaders: true, failures: 1, status: http.StatusOK, wantAttempts: 2},
		{name: "partial JSON", body: `{"id":"stale","name":"discard-me","state":`, failures: 1, status: http.StatusOK, wantAttempts: 2},
		{name: "complete JSON", body: `{"id":"stale","name":"discard-me"}`, failures: 1, status: http.StatusOK, wantAttempts: 2},
		{name: "exhausted", body: `{"id":"stale","name":"discard-me"}`, failures: 3, status: http.StatusOK, wantAttempts: 3},
		{name: "terminal HTTP status", body: `{"detail":"interrupted`, failures: 1, status: http.StatusUnauthorized, wantAttempts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var attempts atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 2 {
					t.Errorf("protocol = %s, want HTTP/2", r.Proto)
				}
				assertNetworkRequest(t, r, http.MethodGet, "/api/deployments/sess_123", "")
				if attempts.Add(1) <= test.failures {
					if !test.beforeHeaders {
						w.WriteHeader(test.status)
						_, _ = io.WriteString(w, test.body)
						w.(http.Flusher).Flush()
					}
					// Aborting the handler sends RST_STREAM(INTERNAL_ERROR).
					panic(http.ErrAbortHandler)
				}
				_, _ = io.WriteString(w, `{"id":"fresh","state":"running"}`)
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			t.Cleanup(server.Close)
			client := NewClient(server.URL, "local-test-token")
			client.OrgID = " org_retry "
			client.HTTP = server.Client()
			client.HTTP.Timeout = 5 * time.Second

			result, err := client.GetSession("sess_123")
			switch {
			case test.status != http.StatusOK:
				if result != nil || !IsStatus(err, test.status) {
					t.Fatalf("result = %+v, error = %v; want HTTP %d", result, err, test.status)
				}
			case test.failures == 3:
				if result != nil || err == nil || !strings.Contains(err.Error(), "INTERNAL_ERROR; received from peer") {
					t.Fatalf("result = %+v, error = %v; want exhausted stream reset with no partial record", result, err)
				}
			default:
				if err != nil || result == nil || result.ID != "fresh" || result.Name != "" || result.State != "running" {
					t.Fatalf("result = %+v, error = %v; want only the recovered record", result, err)
				}
			}
			if got := attempts.Load(); got != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", got, test.wantAttempts)
			}
		})
	}
}

// These tests use real HTTP connections independently of the retry helper.
// Disable connection reuse so net/http's own stale-connection retry cannot
// conceal missing application retries or change the observed attempt count.
func TestNetworkCloudReadsRecoverOnWire(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		path     string
		query    string
		partial  string
		complete string
		check    func(*testing.T, *Client)
	}{
		{
			name:     "session",
			path:     "/api/deployments/sess%2Fwith%20space%3Fx",
			partial:  `{"id":"discard-me","status_reason":"stale","state":`,
			complete: `{"id":"sess/with space?x","state":"healthy","status":"ready"}`,
			check: func(t *testing.T, client *Client) {
				record, err := client.GetSession("sess/with space?x")
				if err != nil {
					t.Fatal(err)
				}
				if record.ID != "sess/with space?x" || record.State != "healthy" || record.Status != "ready" || record.StatusReason != "" {
					t.Fatalf("recovered session contains missing or stale fields: %+v", record)
				}
			},
		},
		{
			name:     "session list",
			path:     "/api/deployments",
			partial:  `{"deployments":[{"id":"discard-me","state":"failed"},`,
			complete: `{"deployments":[{"id":"fresh","state":"healthy"}]}`,
			check: func(t *testing.T, client *Client) {
				records, err := client.ListSessions()
				if err != nil {
					t.Fatal(err)
				}
				if len(records) != 1 || records[0].ID != "fresh" || records[0].State != "healthy" {
					t.Fatalf("recovered list contains missing or stale records: %+v", records)
				}
			},
		},
		{
			name:     "log page with cursors",
			path:     "/api/deployments/sess%2Fwith%20space%3Fx/logs",
			query:    "before_cp=0&before_rt=125&tail=2",
			partial:  `{"events":[{"event":"discard-me","event_id":"stale","event_seq":100},`,
			complete: `{"events":[{"event":"agent_progress","event_id":"evt_101","event_seq":101,"data":{"text":"first"},"extension":"preserve"},{"event":"agent_progress","event_id":"evt_102","event_seq":102,"data":{"text":"second"}}],"cursors":{"rt":101,"cp":0,"session":"runtime-session"}}`,
			check: func(t *testing.T, client *Client) {
				runtime, control := int64(125), int64(0)
				page, err := client.GetSessionLogPageBefore("sess/with space?x", 2, &runtime, &control)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Events) != 2 || len(page.RawEvents) != 2 {
					t.Fatalf("recovered page lost or duplicated events: %+v", page)
				}
				if page.Cursors == nil || page.Cursors.Runtime == nil || *page.Cursors.Runtime != 101 ||
					page.Cursors.Control == nil || *page.Cursors.Control != 0 ||
					!page.Cursors.SessionKnown || page.Cursors.Session == nil || *page.Cursors.Session != "runtime-session" {
					t.Fatalf("retry lost pagination cursors: %+v", page.Cursors)
				}
				for index, event := range page.Events {
					wantSequence := int64(101 + index)
					wantID := fmt.Sprintf("evt_%d", wantSequence)
					if event.EventSeq == nil || *event.EventSeq != wantSequence || event.EventID == nil || *event.EventID != wantID {
						t.Fatalf("event %d lost its identity or order: %+v", index, event)
					}
				}
				wantRaw := []string{
					`{"event":"agent_progress","event_id":"evt_101","event_seq":101,"data":{"text":"first"},"extension":"preserve"}`,
					`{"event":"agent_progress","event_id":"evt_102","event_seq":102,"data":{"text":"second"}}`,
				}
				gotRaw := []string{string(page.RawEvents[0]), string(page.RawEvents[1])}
				if !reflect.DeepEqual(gotRaw, wantRaw) {
					t.Fatalf("retry changed raw events: got %q, want %q", gotRaw, wantRaw)
				}
			},
		},
		{
			name:     "account bootstrap",
			path:     "/api/account/bootstrap",
			partial:  `{"personal_org_id":"org_stale","organizations":[{"id":"org_stale"},`,
			complete: `{"personal_org_id":"org_fresh","organizations":[{"id":"org_fresh","kind":"personal"}]}`,
			check: func(t *testing.T, client *Client) {
				account, err := client.AccountBootstrap()
				if err != nil {
					t.Fatal(err)
				}
				if account.PersonalOrgID != "org_fresh" || len(account.Organizations) != 1 || account.Organizations[0].ID != "org_fresh" {
					t.Fatalf("recovered account contains missing or stale organizations: %+v", account)
				}
			},
		},
	}
	for _, test := range tests {
		for _, failure := range []string{"reset before headers", "reset during body", "truncated content length"} {
			t.Run(test.name+"/"+failure, func(t *testing.T) {
				t.Parallel()
				var attempts atomic.Int32
				headersObserved := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempt := attempts.Add(1)
					assertNetworkRequest(t, r, http.MethodGet, "/proxy"+test.path, test.query)
					if attempt == 1 {
						breakNetworkResponse(t, w, failure, http.StatusOK, test.partial, headersObserved)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, test.complete)
				}))
				t.Cleanup(server.Close)
				client := newNetworkClient(t, server.URL+"/proxy", headersObserved)
				test.check(t, client)
				if got := attempts.Load(); got != 2 {
					t.Fatalf("server received %d requests after one recoverable failure, want 2", got)
				}
			})
		}
	}
}

func TestNetworkCloudReadExhaustionReturnsNoPartialLogs(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	headersObserved := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		assertNetworkRequest(t, r, http.MethodGet, "/api/deployments/sess_123/logs", "tail=2")
		breakNetworkResponse(t, w, "truncated content length", http.StatusOK,
			`{"events":[{"event":"agent_progress","event_id":"partial","event_seq":1},`, headersObserved)
	}))
	t.Cleanup(server.Close)
	client := newNetworkClient(t, server.URL, headersObserved)
	page, err := client.GetSessionLogPage("sess_123", 2)
	if err == nil || page != nil {
		t.Fatalf("exhausted read exposed a partial page or hid the error: page=%+v, err=%v", page, err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("exhausted read lost the body failure: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("persistent body failures made %d requests, want 3", got)
	}
}

func TestNetworkCloudHTTPFailuresStayTerminal(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, brokenBody := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/broken_body=%t", status, brokenBody), func(t *testing.T) {
				t.Parallel()
				var attempts atomic.Int32
				headersObserved := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts.Add(1)
					if brokenBody {
						breakNetworkResponse(t, w, "reset during body", status, `{"detail":"interrupted`, headersObserved)
						return
					}
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"detail":"terminal response"}`)
				}))
				t.Cleanup(server.Close)
				client := newNetworkClient(t, server.URL, headersObserved)
				record, err := client.GetSession("sess_123")
				if record != nil || !IsStatus(err, status) {
					t.Fatalf("HTTP failure lost its status: record=%+v, err=%v, want HTTP %d", record, err, status)
				}
				if got := attempts.Load(); got != 1 {
					t.Fatalf("HTTP %d was retried: got %d requests", status, got)
				}
			})
		}
	}
}

func TestNetworkCloudMutationResponseLossNeverReplays(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
		call   func(*Client) (*SessionRecord, error)
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/api/deployments",
			call: func(client *Client) (*SessionRecord, error) {
				return client.CreateSession(SessionCreateOptions{Name: "test", PackageRef: "@test/package:1.0.0"})
			},
		},
		{
			name:   "update",
			method: http.MethodPut,
			path:   "/api/deployments/sess_123",
			call: func(client *Client) (*SessionRecord, error) {
				return client.UpdateSession("sess_123", SessionUpdateOptions{PackageRef: "@test/package:2.0.0", Force: true})
			},
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			path:   "/api/deployments/sess_123",
			call: func(client *Client) (*SessionRecord, error) {
				return client.DeleteSession("sess_123")
			},
		},
	}
	for _, test := range tests {
		for _, failure := range []string{"reset before headers", "reset during body", "truncated content length"} {
			t.Run(test.name+"/"+failure, func(t *testing.T) {
				t.Parallel()
				var mutations atomic.Int32
				headersObserved := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assertNetworkRequest(t, r, test.method, test.path, "")
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						t.Errorf("read mutation request: %v", err)
					}
					// Model an accepted mutation whose response is then lost.
					if mutations.Add(1) == 1 {
						breakNetworkResponse(t, w, failure, http.StatusOK, `{"id":"sess_123","state":`, headersObserved)
						return
					}
					_, _ = io.WriteString(w, `{"id":"duplicate-mutation","state":"healthy"}`)
				}))
				t.Cleanup(server.Close)
				client := newNetworkClient(t, server.URL, headersObserved)
				record, err := test.call(client)
				if err == nil || record != nil {
					t.Fatalf("lost mutation response did not remain an error: record=%+v, err=%v", record, err)
				}
				if got := mutations.Load(); got != 1 {
					t.Fatalf("accepted %s was replayed after response loss: %d mutations", test.name, got)
				}
			})
		}
	}
}

type adversarialRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip adversarialRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func newNetworkClient(t *testing.T, endpoint string, headersObserved chan struct{}) *Client {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	var firstResponse sync.Once
	client := NewClient(endpoint, "local-test-token")
	client.OrgID = " org_retry "
	client.HTTP = &http.Client{
		Timeout: 5 * time.Second,
		Transport: adversarialRoundTripper(func(request *http.Request) (*http.Response, error) {
			response, err := transport.RoundTrip(request)
			if response != nil {
				firstResponse.Do(func() { close(headersObserved) })
			}
			return response, err
		}),
	}
	return client
}

func assertNetworkRequest(t *testing.T, request *http.Request, method, path, query string) {
	t.Helper()
	if request.Method != method || request.URL.EscapedPath() != path || request.URL.RawQuery != query {
		t.Errorf("request changed: got %s %s, want %s %s with query %q", request.Method, request.RequestURI, method, path, query)
	}
	for name, want := range map[string]string{
		"Authorization":  "Bearer local-test-token",
		"X-Telos-Org-Id": "org_retry",
		"User-Agent":     UserAgent,
	} {
		if got := request.Header.Get(name); got != want {
			t.Errorf("%s changed: got %q, want %q", name, got, want)
		}
	}
}

func breakNetworkResponse(t *testing.T, writer http.ResponseWriter, failure string, status int, partial string, headersObserved <-chan struct{}) {
	t.Helper()
	connection, buffered, err := writer.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("hijack test connection: %v", err)
		return
	}
	defer connection.Close()
	if failure != "reset before headers" {
		_, err = fmt.Fprintf(buffered, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), len(partial)+100, partial)
		if err == nil {
			err = buffered.Flush()
		}
		if err != nil {
			t.Errorf("write partial response: %v", err)
			return
		}
		// Ensure headers reached the client before cutting the connection so
		// these cases exercise response-body recovery, not just HTTP.Do errors.
		select {
		case <-headersObserved:
		case <-time.After(5 * time.Second):
			t.Error("client did not receive response headers")
			return
		}
	}
	if failure != "truncated content length" {
		tcp, ok := connection.(*net.TCPConn)
		if !ok {
			t.Errorf("test connection is %T, want *net.TCPConn", connection)
			return
		}
		if err := tcp.SetLinger(0); err != nil {
			t.Errorf("configure TCP reset: %v", err)
		}
	}
}
