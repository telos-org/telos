package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

func TestDeriveCloudSessionProgressUsesStructuredEvents(t *testing.T) {
	prepareAt := "2026-08-10T12:00:00Z"
	claimAt := "2026-08-10T12:02:00Z"
	progressAt := "2026-08-10T12:04:30Z"
	latestAt := "2026-08-10T12:04:50Z"
	source := "agent"
	session := &cloud.SessionRecord{
		ID:        "sess_123",
		State:     "deploying",
		CreatedAt: "2026-08-10T11:59:00Z",
	}
	events := []sessionapi.SessionEvent{
		{
			Event:     "runtime.prepare.started",
			Timestamp: &prepareAt,
			Data: map[string]any{
				"stage":            "prepare",
				"message":          "Restoring guest",
				"runtime_provider": "gce",
				"allocation_id":    "alloc_123",
				"host_id":          "host_9",
			},
		},
		{
			Event:     "runtime.claim.succeeded",
			Timestamp: &claimAt,
			Data:      map[string]any{"message": "Runtime claimed"},
		},
		{
			Event:     "agent_progress",
			Timestamp: &progressAt,
			Source:    &source,
			Data:      map[string]any{"kind": "progress_update", "text": "Applying the workload"},
		},
		{
			Event:     "agent_progress",
			Timestamp: &latestAt,
			Source:    &source,
			Data:      map[string]any{"kind": "progress_update", "text": "Running integration tests"},
		},
	}

	progress := deriveCloudSessionProgress(
		session,
		events,
		time.Date(2026, 8, 10, 12, 5, 0, 0, time.UTC),
	)
	if progress.Stage != "agent execution" || progress.StageSince != progressAt {
		t.Fatalf("stage = %#v", progress)
	}
	if progress.StageAgeSeconds == nil || *progress.StageAgeSeconds != 30 {
		t.Fatalf("stage age = %#v", progress.StageAgeSeconds)
	}
	if progress.LatestActivity != "Running integration tests" || progress.LatestActivityAt != latestAt {
		t.Fatalf("latest activity = %#v", progress)
	}
	if progress.LatestActivityAgeSeconds == nil || *progress.LatestActivityAgeSeconds != 10 {
		t.Fatalf("activity age = %#v", progress.LatestActivityAgeSeconds)
	}
	if progress.RuntimeProvider != "gce" || progress.Allocation != "alloc_123" || progress.Host != "host_9" {
		t.Fatalf("runtime details = %#v", progress)
	}
}

func TestDeriveCloudSessionProgressSurfacesWorkloadBlocker(t *testing.T) {
	waitingAt := "2026-08-10T12:00:00Z"
	session := &cloud.SessionRecord{State: "deploying", CreatedAt: waitingAt}
	events := []sessionapi.SessionEvent{{
		Event:     "workload.rollout.waiting",
		Timestamp: &waitingAt,
		Data: map[string]any{
			"stage":        "workload",
			"state":        "waiting",
			"blocker_code": "no-default-storage-class",
			"message":      "PVC pending",
		},
	}}

	progress := deriveCloudSessionProgress(session, events, time.Date(2026, 8, 10, 12, 1, 0, 0, time.UTC))
	if progress.Stage != "workload rollout" {
		t.Fatalf("stage = %q", progress.Stage)
	}
	if progress.WaitingReason != "no-default-storage-class" {
		t.Fatalf("waiting reason = %q", progress.WaitingReason)
	}
	if progress.LatestActivity != "PVC pending" {
		t.Fatalf("latest activity = %q", progress.LatestActivity)
	}
}

func TestPrintCloudSessionDescriptionShowsProgressAndVerboseRuntimeDetails(t *testing.T) {
	session := cloud.SessionRecord{
		ID:            "sess_123",
		Name:          "demo",
		State:         "deploying",
		PackageRef:    "@telos/demo:1.2.3",
		PackageDigest: "sha256:abc",
		CreatedAt:     "then",
		UpdatedAt:     "now",
	}
	stageAge := int64(90)
	activityAge := int64(12)
	progress := cloudSessionProgress{
		Stage:                    "workload rollout",
		StageAgeSeconds:          &stageAge,
		LatestActivity:           "PVC pending",
		LatestActivityAgeSeconds: &activityAge,
		WaitingReason:            "no-default-storage-class",
		RuntimeProvider:          "gce",
		Allocation:               "alloc_123",
		Host:                     "host_9",
	}

	var out bytes.Buffer
	printCloudSessionDescriptionWithProgress(
		&out,
		session,
		"org_telos",
		progress,
		true,
		nil,
	)
	for _, want := range []string{
		"Stage     workload rollout",
		"Stage for 1m 30s",
		"Latest    PVC pending",
		"Activity age 12s",
		"Service   pending",
		"Dashboard pending",
		"Waiting   no-default-storage-class",
		"Runtime provider gce",
		"Allocation alloc_123",
		"Host      host_9",
		"Inspect   telos logs --context org_telos sess_123",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("description missing %q:\n%s", want, out.String())
		}
	}
}
