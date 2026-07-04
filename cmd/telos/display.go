package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/telos-org/telos/internal/sessionapi"
)

type sessionDisplayRow struct {
	Name    string
	Target  string
	Status  string
	Cost    string
	Session string
	Parent  string
}

func displayRow(sess sessionapi.Session) sessionDisplayRow {
	return sessionDisplayRow{
		Name:    sessionName(sess),
		Target:  sessionTarget(sess),
		Status:  sessionDisplayStatus(sess),
		Cost:    sessionCost(sess),
		Session: sess.SessionID,
		Parent:  sessionParent(sess),
	}
}

func sessionTarget(sess sessionapi.Session) string {
	if sess.Runtime != "" {
		return string(sess.Runtime)
	}
	return "-"
}

func sessionDisplayStatus(sess sessionapi.Session) string {
	switch sess.Status {
	case sessionapi.StatusCompleted:
		return "completed"
	case sessionapi.StatusFailed:
		return "failed"
	case sessionapi.StatusStopped:
		return "stopped"
	case sessionapi.StatusStale:
		return "stale"
	}
	if hasActiveTurn(sess) || hasOpenEpoch(sess) {
		return "active"
	}
	if retainedTopLevelCloudSession(sess) {
		return "idle"
	}
	switch sess.Status {
	case sessionapi.StatusPending, sessionapi.StatusRunning:
		return "active"
	default:
		if sess.Status != "" {
			return string(sess.Status)
		}
		return "-"
	}
}

func retainedTopLevelCloudSession(sess sessionapi.Session) bool {
	return isTopLevelSession(sess) &&
		sess.Runtime == sessionapi.RuntimeCloud &&
		sessionResult(sess) == "completed"
}

func hasActiveTurn(sess sessionapi.Session) bool {
	return sess.CurrentRound != nil && sess.CurrentRole != nil && *sess.CurrentRole != ""
}

func hasOpenEpoch(sess sessionapi.Session) bool {
	for _, epoch := range sess.Epochs {
		if epochValueEmpty(epoch["finished_at"]) && epochValueEmpty(epoch["result"]) {
			return true
		}
	}
	return false
}

func epochValueEmpty(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func fileURI(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if runtime.GOOS == "windows" {
		abs = "/" + strings.ReplaceAll(abs, `\`, `/`)
	}
	u := url.URL{Scheme: "file", Path: abs}
	return u.String()
}

func formatDetailCost(value *float64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("$%.4f", *value)
}
