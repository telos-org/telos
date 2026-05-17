package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

// DaemonError is returned when telosd answers a request with an HTTP error.
type DaemonError struct {
	Status int
	Detail string
}

func (e *DaemonError) Error() string {
	return fmt.Sprintf("telosd request failed (HTTP %d): %s", e.Status, e.Detail)
}

// Client talks to the telosd daemon for one workspace over its Unix socket,
// starting the daemon on demand.
type Client struct {
	workspaceRoot string
	paths         DaemonPaths
	http          *http.Client
}

// NewClient returns a telosd client for a workspace root.
func NewClient(workspaceRoot string) *Client {
	root := absOr(workspaceRoot)
	paths := DaemonPathsFor(root)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
		},
	}
	return &Client{
		workspaceRoot: root,
		paths:         paths,
		http:          &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// CreateSession submits a new session and returns it.
func (c *Client) CreateSession(req sessionapi.SessionCreateRequest) (*sessionapi.Session, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var session sessionapi.Session
	if err := c.do(http.MethodPost, "/api/sessions", body, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// ListSessions returns all sessions known to the daemon.
func (c *Client) ListSessions() ([]sessionapi.Session, error) {
	var resp sessionapi.SessionListResponse
	if err := c.do(http.MethodGet, "/api/sessions", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

// GetSession returns a single session by ID.
func (c *Client) GetSession(id string) (*sessionapi.Session, error) {
	var session sessionapi.Session
	if err := c.do(http.MethodGet, "/api/sessions/"+id, nil, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// StopSession stops a running session.
func (c *Client) StopSession(id string) (*sessionapi.Session, error) {
	var session sessionapi.Session
	if err := c.do(http.MethodPost, "/api/sessions/"+id+"/stop", nil, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// Transcript returns the PVG transcript for a session.
func (c *Client) Transcript(id string) (string, error) {
	raw, err := c.request(http.MethodGet, "/api/sessions/"+id+"/transcript", nil)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Events returns the evidence events for a session.
func (c *Client) Events(id string) ([]sessionapi.SessionEvent, error) {
	var resp sessionapi.SessionEventsResponse
	if err := c.do(http.MethodGet, "/api/sessions/"+id+"/events", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Events, nil
}

func (c *Client) do(method, path string, body []byte, out any) error {
	raw, err := c.request(method, path, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) request(method, path string, body []byte) ([]byte, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://telosd"+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telosd request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &DaemonError{Status: resp.StatusCode, Detail: errorDetail(raw)}
	}
	return raw, nil
}

// ensure guarantees a healthy telosd for the workspace, starting one if
// needed. Concurrent callers are serialized by a file lock so only one
// process spawns the daemon.
func (c *Client) ensure() error {
	if err := os.MkdirAll(c.paths.RunDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(c.paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	if c.healthy() {
		return nil
	}
	if err := c.cleanupDeadDaemon(); err != nil {
		return err
	}
	if err := c.spawnDaemon(); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.healthy() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("telosd did not start; see %s", c.paths.Log)
}

func (c *Client) healthy() bool {
	if _, err := os.Stat(c.paths.Socket); err != nil {
		return false
	}
	probe := &http.Client{Timeout: time.Second, Transport: c.http.Transport}
	resp, err := probe.Get("http://telosd/api/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func (c *Client) cleanupDeadDaemon() error {
	if pid := daemonPID(c.paths.PID); pid > 0 && pidAlive(pid) {
		return fmt.Errorf("telosd is running but not responding (pid %d); see %s", pid, c.paths.Log)
	}
	os.Remove(c.paths.Socket)
	os.Remove(c.paths.PID)
	return nil
}

func (c *Client) spawnDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate telos binary: %w", err)
	}
	logFile, err := os.OpenFile(c.paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, "serve", "--workspace-root", c.workspaceRoot)
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn telosd: %w", err)
	}
	return cmd.Process.Release()
}

func daemonPID(pidPath string) int {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0
	}
	var pf pidFile
	if json.Unmarshal(data, &pf) != nil {
		return 0
	}
	return pf.PID
}

func errorDetail(raw []byte) string {
	var payload struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &payload) == nil && payload.Detail != "" {
		return payload.Detail
	}
	if len(raw) > 0 {
		return string(raw)
	}
	return "no response body"
}
