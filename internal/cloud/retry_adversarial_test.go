package cloud

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests use real HTTP connections independently of the retry helper.
// Disable connection reuse so net/http's own stale-connection retry cannot
// conceal missing application retries or change the observed attempt count.
func TestAdversarialCloudReadsRecoverOnWire(t *testing.T) {
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
			name:     "log page",
			path:     "/api/deployments/sess%2Fwith%20space%3Fx/logs",
			query:    "tail=2",
			partial:  `{"events":[{"event":"discard-me","event_id":"stale","event_seq":100},`,
			complete: `{"events":[{"event":"agent_progress","event_id":"evt_101","event_seq":101,"data":{"text":"first"},"extension":"preserve"},{"event":"agent_progress","event_id":"evt_102","event_seq":102,"data":{"text":"second"}}]}`,
			check: func(t *testing.T, client *Client) {
				page, err := client.GetSessionLogPage("sess/with space?x", 2)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Events) != 2 || len(page.RawEvents) != 2 {
					t.Fatalf("recovered page lost or duplicated events: %+v", page)
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
					assertAdversarialRequest(t, r, http.MethodGet, "/proxy"+test.path, test.query)
					if attempt == 1 {
						breakAdversarialResponse(t, w, failure, http.StatusOK, test.partial, headersObserved)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, test.complete)
				}))
				t.Cleanup(server.Close)
				client := newAdversarialClient(t, server.URL+"/proxy", headersObserved)
				test.check(t, client)
				if got := attempts.Load(); got != 2 {
					t.Fatalf("server received %d requests after one recoverable failure, want 2", got)
				}
			})
		}
	}
}

func TestAdversarialCloudReadExhaustionReturnsNoPartialLogs(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	headersObserved := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		assertAdversarialRequest(t, r, http.MethodGet, "/api/deployments/sess_123/logs", "tail=2")
		breakAdversarialResponse(t, w, "truncated content length", http.StatusOK,
			`{"events":[{"event":"agent_progress","event_id":"partial","event_seq":1},`, headersObserved)
	}))
	t.Cleanup(server.Close)
	client := newAdversarialClient(t, server.URL, headersObserved)
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

func TestAdversarialCloudHTTPFailuresStayTerminal(t *testing.T) {
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
						breakAdversarialResponse(t, w, "reset during body", status, `{"detail":"interrupted`, headersObserved)
						return
					}
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"detail":"terminal response"}`)
				}))
				t.Cleanup(server.Close)
				client := newAdversarialClient(t, server.URL, headersObserved)
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

func TestAdversarialCloudMutationResponseLossNeverReplays(t *testing.T) {
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
					assertAdversarialRequest(t, r, test.method, test.path, "")
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						t.Errorf("read mutation request: %v", err)
					}
					// Model an accepted mutation whose response is then lost.
					if mutations.Add(1) == 1 {
						breakAdversarialResponse(t, w, failure, http.StatusOK, `{"id":"sess_123","state":`, headersObserved)
						return
					}
					_, _ = io.WriteString(w, `{"id":"duplicate-mutation","state":"healthy"}`)
				}))
				t.Cleanup(server.Close)
				client := newAdversarialClient(t, server.URL, headersObserved)
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

func newAdversarialClient(t *testing.T, endpoint string, headersObserved chan struct{}) *Client {
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

func assertAdversarialRequest(t *testing.T, request *http.Request, method, path, query string) {
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

func breakAdversarialResponse(t *testing.T, writer http.ResponseWriter, failure string, status int, partial string, headersObserved <-chan struct{}) {
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
