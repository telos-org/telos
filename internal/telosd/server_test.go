package telosd

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWithCloudCORSAllowsControlPlaneOrigins(t *testing.T) {
	handler := withCloudCORS(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Origin", "https://app.usetelos.ai")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status: got %d", res.Code)
	}
	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "https://app.usetelos.ai" {
		t.Fatalf("allow origin: got %q", got)
	}
	if got := res.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("allow credentials: got %q", got)
	}
	if got := res.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("vary: got %q", got)
	}
}

func TestWithCloudCORSAllowsLocalhostOrigins(t *testing.T) {
	handler := withCloudCORS(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("allow origin: got %q", got)
	}
}

func TestWithCloudCORSRejectsUnknownOrigins(t *testing.T) {
	handler := withCloudCORS(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	req.Header.Set("Origin", "https://example.com")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow origin: got %q", got)
	}
}

func TestWithCloudCORSHandlesPreflight(t *testing.T) {
	handler := withCloudCORS(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/sessions", nil)
	req.Header.Set("Origin", "https://env-test.usetelos.ai")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status: got %d", res.Code)
	}
	if got := res.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, OPTIONS" {
		t.Fatalf("allow methods: got %q", got)
	}
	if got := res.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type, Accept, X-Telos-User-Authorization, X-Telos-Org-Id" {
		t.Fatalf("allow headers: got %q", got)
	}
}

func TestShutdownHTTPServerClosesStalledConnectionsAfterGrace(t *testing.T) {
	tracker := newServerConnTracker()
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		ReadHeaderTimeout: time.Minute,
		IdleTimeout:       time.Minute,
		ConnState:         tracker.ConnState,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForTrackedConn(t, tracker)

	err = shutdownHTTPServer(srv, tracker, 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected shutdown deadline from stalled connection")
	}
	if setErr := conn.SetReadDeadline(time.Now().Add(time.Second)); setErr != nil {
		t.Fatal(setErr)
	}
	buf := []byte{0}
	_, readErr := conn.Read(buf)
	if readErr == nil {
		t.Fatal("stalled connection remained open after forced close")
	}
	if !errors.Is(readErr, io.EOF) {
		var netErr net.Error
		if !errors.As(readErr, &netErr) || netErr.Timeout() {
			t.Fatalf("unexpected read error after forced close: %v", readErr)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func waitForTrackedConn(t *testing.T, tracker *serverConnTracker) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tracker.mu.Lock()
		n := len(tracker.conns)
		tracker.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("connection was not tracked")
}
