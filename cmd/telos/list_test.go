package main

import (
	"testing"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

func TestVisibleListSessionsHidesChildSessionsByDefault(t *testing.T) {
	parent := "sess_parent"
	sessions := []sessionapi.Session{
		{SessionID: "sess_controller", Status: sessionapi.StatusRunning},
		{SessionID: "sess_task", ParentSessionID: &parent, Status: sessionapi.StatusCompleted},
		{SessionID: "sess_controller_2", Status: sessionapi.StatusScheduled},
	}

	visible := visibleListSessions(sessions, false)
	if len(visible) != 2 {
		t.Fatalf("visible session count: got %d, want 2", len(visible))
	}
	if visible[0].SessionID != "sess_controller" || visible[1].SessionID != "sess_controller_2" {
		t.Fatalf("visible sessions: got %#v", visible)
	}
}

func TestVisibleListSessionsWideKeepsChildSessions(t *testing.T) {
	parent := "sess_parent"
	sessions := []sessionapi.Session{
		{SessionID: "sess_controller", Status: sessionapi.StatusRunning},
		{SessionID: "sess_task", ParentSessionID: &parent, Status: sessionapi.StatusCompleted},
	}

	visible := visibleListSessions(sessions, true)
	if len(visible) != len(sessions) {
		t.Fatalf("wide session count: got %d, want %d", len(visible), len(sessions))
	}
	if visible[1].SessionID != "sess_task" {
		t.Fatalf("wide sessions should preserve child rows: got %#v", visible)
	}
}
