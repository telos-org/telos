package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/sessionapi"
)

type cloudSessionProgress struct {
	Stage                    string `json:"stage"`
	StageSince               string `json:"stage_since,omitempty"`
	StageAgeSeconds          *int64 `json:"stage_age_seconds,omitempty"`
	LatestActivity           string `json:"latest_activity,omitempty"`
	LatestActivityAt         string `json:"latest_activity_at,omitempty"`
	LatestActivityAgeSeconds *int64 `json:"latest_activity_age_seconds,omitempty"`
	WaitingReason            string `json:"waiting_reason,omitempty"`
	RuntimeProvider          string `json:"runtime_provider,omitempty"`
	Allocation               string `json:"allocation,omitempty"`
	Host                     string `json:"host,omitempty"`
}

func loadCloudSessionProgress(
	session *cloud.SessionRecord,
	contextOverride string,
	now time.Time,
) (cloudSessionProgress, error) {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return deriveCloudSessionProgress(session, nil, now), err
	}
	page, err := control.GetSessionLogPage(session.ID)
	if err != nil {
		return deriveCloudSessionProgress(session, nil, now), err
	}
	return deriveCloudSessionProgress(session, page.Events, now), nil
}

func deriveCloudSessionProgress(
	session *cloud.SessionRecord,
	events []sessionapi.SessionEvent,
	now time.Time,
) cloudSessionProgress {
	progress := cloudSessionProgress{
		Stage: fallbackProductStage(session.State),
	}
	if session.FailureReason != nil {
		progress.WaitingReason = strings.TrimSpace(*session.FailureReason)
	}

	for _, event := range events {
		if stage := eventProductStage(event); stage != "" {
			if stage != progress.Stage {
				progress.WaitingReason = ""
				progress.Stage = stage
				progress.StageSince = eventTimestamp(event)
			} else if progress.StageSince == "" {
				progress.StageSince = eventTimestamp(event)
			}
		}

		if row, ok := renderedLogRowFromEvent(event, false); ok {
			progress.LatestActivity = row.Summary
			if row.Detail != "" && !row.MultilineDetail {
				progress.LatestActivity += " — " + row.Detail
			}
			progress.LatestActivityAt = row.Timestamp
			progress.WaitingReason = structuredWaitingReason(event)
		}

		progress.RuntimeProvider = latestEventValue(
			progress.RuntimeProvider,
			event,
			"runtime_provider",
			"provider",
		)
		progress.Allocation = latestEventValue(
			progress.Allocation,
			event,
			"allocation_id",
			"allocation",
		)
		progress.Host = latestEventValue(
			progress.Host,
			event,
			"host_id",
			"host",
		)
	}

	if session.FailureReason != nil && strings.TrimSpace(*session.FailureReason) != "" {
		progress.WaitingReason = strings.TrimSpace(*session.FailureReason)
	}
	progress.StageAgeSeconds = eventAgeSeconds(progress.StageSince, now)
	progress.LatestActivityAgeSeconds = eventAgeSeconds(progress.LatestActivityAt, now)
	return progress
}

func eventProductStage(event sessionapi.SessionEvent) string {
	if stage := normalizeProductStage(eventDataString(event, "stage")); stage != "" {
		return stage
	}
	return normalizeProductStage(event.Event)
}

func normalizeProductStage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case value == "healthy", strings.Contains(value, "health.succeeded"), strings.Contains(value, "healthy"):
		return "healthy"
	case strings.Contains(value, "checkpoint"):
		return "checkpointing"
	case strings.Contains(value, "guest") && strings.Contains(value, "restore"):
		return "guest restore"
	case strings.Contains(value, "restor"):
		return "restoring"
	case strings.Contains(value, "allocat"), strings.Contains(value, "schedul"):
		return "allocation"
	case strings.Contains(value, "claim"):
		return "runtime claim"
	case strings.Contains(value, "agent"), strings.HasPrefix(value, "game_"), strings.HasPrefix(value, "round_"):
		return "agent execution"
	case strings.Contains(value, "workload"), strings.Contains(value, "rollout"), strings.Contains(value, "deploy"):
		return "workload rollout"
	case strings.Contains(value, "route"):
		return "route publication"
	case strings.Contains(value, "prepare"), strings.Contains(value, "boot"):
		return "guest restore"
	default:
		return ""
	}
}

func fallbackProductStage(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "provisioning":
		return "allocation"
	case "deploying":
		return "workload rollout"
	case "healthy":
		return "healthy"
	case "deleting", "deleted", "stopped", "failed":
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return "unknown"
	}
}

func structuredWaitingReason(event sessionapi.SessionEvent) string {
	state := strings.ToLower(eventDataString(event, "state"))
	name := strings.ToLower(strings.TrimSpace(event.Event))
	waiting := state == "waiting" || state == "failed" ||
		strings.HasSuffix(name, ".waiting") ||
		strings.HasSuffix(name, ".failed") ||
		strings.HasSuffix(name, ".error") ||
		name == "agent_failure_recoverable" || name == "game_error"
	if !waiting {
		return ""
	}
	return firstNonEmpty(
		eventDataString(event, "blocker_code"),
		eventDataString(event, "blocker"),
		eventDataString(event, "reason"),
		eventDataString(event, "error"),
		eventDataString(event, "message"),
	)
}

func latestEventValue(current string, event sessionapi.SessionEvent, keys ...string) string {
	for _, key := range keys {
		if value := eventDataString(event, key); value != "" {
			return value
		}
	}
	return current
}

func eventAgeSeconds(value string, now time.Time) *int64 {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	seconds := int64(now.Sub(parsed).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return &seconds
}

func formatAge(seconds *int64) string {
	if seconds == nil {
		return "unknown"
	}
	duration := time.Duration(*seconds) * time.Second
	switch {
	case duration < time.Minute:
		return fmt.Sprintf("%ds", *seconds)
	case duration < time.Hour:
		return fmt.Sprintf("%dm %ds", int(duration.Minutes()), int(duration.Seconds())%60)
	case duration < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(duration.Hours()), int(duration.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(duration.Hours())/24, int(duration.Hours())%24)
	}
}
