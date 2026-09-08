package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type readReviewBody struct {
	io.Reader
	closed bool
}

func (body *readReviewBody) Close() error {
	body.closed = true
	return nil
}

type readReviewTruncatedBody struct {
	data string
}

func (body *readReviewTruncatedBody) Read(p []byte) (int, error) {
	n := copy(p, body.data)
	body.data = body.data[n:]
	if len(body.data) > 0 {
		return n, nil
	}
	return n, io.ErrUnexpectedEOF
}

type readReviewStalledBody struct {
	ctx context.Context
}

func (body readReviewStalledBody) Read([]byte) (int, error) {
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
				body := &readReviewBody{Reader: strings.NewReader(tt.body)}
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
		firstBody := &readReviewBody{Reader: &readReviewTruncatedBody{
			data: `{"id":"first","name":"discarded","failure_reason":"stale"}`,
		}}
		secondBody := &readReviewBody{Reader: strings.NewReader(`{"id":"second"}`)}
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
				var body *readReviewBody
				client := NewClient("https://api.example.test", "test-token")
				client.HTTP.Timeout = time.Second
				client.HTTP.Transport = readRetryTransport(func(req *http.Request) (*http.Response, error) {
					attempts++
					body = &readReviewBody{Reader: readReviewStalledBody{ctx: req.Context()}}
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
