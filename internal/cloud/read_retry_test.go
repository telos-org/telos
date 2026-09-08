package cloud

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
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
		{"aborted", syscall.ECONNABORTED, 3},
		{"broken_pipe", syscall.EPIPE, 3},
		{"eof", io.EOF, 3},
		{"truncated_body", io.ErrUnexpectedEOF, 3},
		{"timeout", &net.DNSError{Err: "temporary timeout", IsTimeout: true}, 3},
		// Transport timeouts can match DeadlineExceeded before the operation's
		// deadline expires (for example, ResponseHeaderTimeout).
		{"transport_deadline", &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}, 3},
		{"canceled", context.Canceled, 1},
		{"unknown_host", &net.DNSError{Err: "no such host", IsNotFound: true}, 1},
		{"certificate", x509.UnknownAuthorityError{}, 1},
		{"invalid_json", &json.SyntaxError{}, 1},
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
	synctest.Test(t, func(t *testing.T) {
		var starts []time.Time
		client := NewClient("https://api.example.test", "")
		client.HTTP.Transport = readRetryTransport(func(*http.Request) (*http.Response, error) {
			starts = append(starts, time.Now())
			if len(starts) < 3 {
				return nil, syscall.ECONNRESET
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
