package executor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/telos-org/telos/internal/agentsession"
)

type SessionReplayReport struct {
	Path                 string         `json:"path"`
	Events               int            `json:"events"`
	EventTypes           map[string]int `json:"event_types"`
	ModelRequests        int            `json:"model_requests"`
	ModelResponses       int            `json:"model_responses"`
	ToolCalls            int            `json:"tool_calls"`
	ToolResults          int            `json:"tool_results"`
	ToolErrors           int            `json:"tool_errors"`
	ToolNonzeroExits     int            `json:"tool_nonzero_exits"`
	ToolTruncated        int            `json:"tool_truncated"`
	UnmatchedToolCalls   int            `json:"unmatched_tool_calls"`
	UnmatchedToolResults int            `json:"unmatched_tool_results"`
	ReasoningSanitized   int            `json:"reasoning_sanitized"`
	FinalAssistantText   string         `json:"final_assistant_text,omitempty"`
	ProtocolOK           bool           `json:"protocol_ok"`
	ProtocolError        string         `json:"protocol_error,omitempty"`
}

func ReplaySessionLog(path, role string) (SessionReplayReport, error) {
	report := SessionReplayReport{
		Path:       path,
		EventTypes: map[string]int{},
		ProtocolOK: true,
	}
	f, err := os.Open(path)
	if err != nil {
		return report, err
	}
	defer f.Close()

	var task string
	var protocolMode string
	var usedTool bool
	toolCalls := map[string]string{}
	toolResults := map[string]string{}
	nonzeroExitByCall := map[string]bool{}
	truncatedByCall := map[string]bool{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var event sessionEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return report, fmt.Errorf("parse session event %d: %w", report.Events+1, err)
		}
		report.Events++
		report.EventTypes[event.Type]++
		switch event.Type {
		case agentsession.KindTurnPolicy:
			if p, err := agentsession.Unmarshal[agentsession.TurnPolicyPayload](&event); err == nil && p.ProtocolMode != "" {
				protocolMode = p.ProtocolMode
			}
		case agentsession.KindModelRequest:
			report.ModelRequests++
		case agentsession.KindModelResponse:
			report.ModelResponses++
		case agentsession.KindToolCall:
			report.ToolCalls++
			usedTool = true
			if p, err := agentsession.Unmarshal[agentsession.ToolCallPayload](&event); err == nil && p.ToolCallID != "" {
				toolCalls[p.ToolCallID] = p.ToolName
			}
		case agentsession.KindToolResult:
			report.ToolResults++
			var id string
			if p, err := agentsession.Unmarshal[agentsession.ToolResultPayload](&event); err == nil {
				id = p.ToolCallID
				if id != "" {
					toolResults[id] = p.ToolName
				}
				if p.IsError {
					report.ToolErrors++
				}
				if p.ExitCode != 0 {
					report.ToolNonzeroExits++
					if id != "" {
						nonzeroExitByCall[id] = true
					}
				}
				if p.Truncated {
					report.ToolTruncated++
					if id != "" {
						truncatedByCall[id] = true
					}
				}
			}
		case agentsession.KindReasoningSanitized:
			report.ReasoningSanitized++
		case agentsession.KindMessage:
			if event.Message == nil {
				continue
			}
			switch event.Message.Role {
			case "user":
				if task == "" {
					task = messageText(event.Message)
				}
			case "assistant":
				report.FinalAssistantText = messageText(event.Message)
			case "toolResult":
				if report.EventTypes["tool_result"] == 0 {
					report.ToolResults++
					if event.Message.ToolCallID != "" {
						toolResults[event.Message.ToolCallID] = event.Message.ToolName
					}
					if event.Message.IsError {
						report.ToolErrors++
					}
				}
				trace := parseToolTrace(messageText(event.Message))
				if trace.NonzeroExit && !nonzeroExitByCall[event.Message.ToolCallID] {
					report.ToolNonzeroExits++
					if event.Message.ToolCallID != "" {
						nonzeroExitByCall[event.Message.ToolCallID] = true
					}
				}
				if trace.Truncated && !truncatedByCall[event.Message.ToolCallID] {
					report.ToolTruncated++
					if event.Message.ToolCallID != "" {
						truncatedByCall[event.Message.ToolCallID] = true
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return report, err
	}
	if prompt, key := protocolCorrectionFor(role, protocolMode, task, report.FinalAssistantText, usedTool); prompt != "" {
		report.ProtocolOK = false
		report.ProtocolError = key
	}
	for callID := range toolCalls {
		if _, ok := toolResults[callID]; !ok {
			report.UnmatchedToolCalls++
		}
	}
	for resultID := range toolResults {
		if _, ok := toolCalls[resultID]; !ok {
			report.UnmatchedToolResults++
		}
	}
	if report.UnmatchedToolCalls > 0 || report.UnmatchedToolResults > 0 {
		report.ProtocolOK = false
		if report.ProtocolError == "" {
			report.ProtocolError = "tool_trace_mismatch"
		}
	}
	return report, nil
}

func messageText(msg *sessionMessage) string {
	if msg == nil {
		return ""
	}
	var out string
	for _, content := range msg.Content {
		if content.Type == "text" {
			if out != "" {
				out += "\n"
			}
			out += content.Text
		}
	}
	return out
}

type replayToolTrace struct {
	NonzeroExit bool
	Truncated   bool
}

func parseToolTrace(text string) replayToolTrace {
	var trace replayToolTrace
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "exit_code":
			n, err := strconv.Atoi(value)
			if err == nil && n != 0 {
				trace.NonzeroExit = true
			}
		case "stdout_truncated", "stderr_truncated", "truncated":
			if strings.EqualFold(value, "true") {
				trace.Truncated = true
			}
		}
	}
	return trace
}
