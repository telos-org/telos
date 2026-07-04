package sessionapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/telos-org/telos/internal/agentsession"
)

type ProtocolEventsOptions struct {
	Raw           bool
	SinceSequence int64
}

func (fs *FileStore) ProtocolEvents(id string, opts ProtocolEventsOptions) ([]agentsession.Event, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	m, err := ReadManifest(fs.manifestPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		return nil, err
	}
	events, err := fs.protocolEventsLocked(id, m, opts.Raw)
	if err != nil {
		return nil, err
	}
	if opts.SinceSequence <= 0 {
		return events, nil
	}
	filtered := events[:0]
	for _, event := range events {
		if event.Sequence > opts.SinceSequence {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}

func (fs *FileStore) protocolEventsLocked(id string, m *Manifest, raw bool) ([]agentsession.Event, error) {
	var events []agentsession.Event
	for _, spec := range m.Specs {
		if spec.DirName == "" {
			continue
		}
		pattern := filepath.Join(fs.sessionDir(id), "specs", spec.DirName, "turns", "*", "session.jsonl")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, path := range matches {
			read, err := readProtocolSessionLog(path, raw)
			if err != nil {
				continue
			}
			events = append(events, read...)
		}
	}

	storeEvents, err := readStoreEvents(fs.storeEventLogPath(id))
	if err != nil {
		return nil, err
	}
	for _, event := range storeEvents {
		events = append(events, storeEventToProtocol(event))
	}
	if !hasTerminalProtocolEvent(events) {
		if terminal, ok := manifestTerminalProtocolEvent(m); ok {
			events = append(events, terminal)
		}
	}
	for i := range events {
		events[i] = normalizeProtocolEvent(events[i])
		events[i].Sequence = int64(i + 1)
		if events[i].ID == "" {
			events[i].ID = fmt.Sprintf("%d", events[i].Sequence)
		}
	}
	return events, nil
}

func readProtocolSessionLog(path string, raw bool) ([]agentsession.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var out []agentsession.Event
	var lastToolResult *agentsession.ToolCallResultStreamPayload
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event agentsession.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		converted := protocolEventsFromNative(event, raw, lastToolResult)
		if len(converted) > 0 {
			out = append(out, converted...)
		}
		lastToolResult = nil
		if event.Type == agentsession.KindToolResult {
			if p, err := agentsession.Unmarshal[agentsession.ToolResultPayload](&event); err == nil {
				stream := toolResultPayloadToStream(*p, raw)
				lastToolResult = &stream
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func protocolEventsFromNative(event agentsession.Event, raw bool, previousToolResult *agentsession.ToolCallResultStreamPayload) []agentsession.Event {
	switch event.Type {
	case agentsession.KindMessage:
		if event.Message == nil {
			return nil
		}
		switch event.Message.Role {
		case "assistant":
			var events []agentsession.Event
			text := messageText(event.Message)
			if text != "" {
				events = append(events, protocolPayloadEvent(event, agentsession.KindAssistantText, agentsession.AssistantTextPayload{
					Text:       text,
					Provider:   event.Message.Provider,
					Model:      event.Message.Model,
					StopReason: event.Message.StopReason,
				}))
			}
			if event.Message.Usage != nil {
				events = append(events, protocolPayloadEvent(event, agentsession.KindUsage, usagePayloadFromMessage(event.Message)))
			}
			return events
		default:
			return nil
		}
	case agentsession.KindToolCall:
		p, err := agentsession.Unmarshal[agentsession.ToolCallPayload](&event)
		if err != nil {
			return nil
		}
		if !raw {
			p.Arguments = ""
			p.Redacted = true
			p.Redaction = map[string]any{"arguments": true}
		}
		return []agentsession.Event{protocolPayloadEvent(event, agentsession.KindToolCallStart, p)}
	case agentsession.KindToolResult:
		p, err := agentsession.Unmarshal[agentsession.ToolResultPayload](&event)
		if err != nil {
			return nil
		}
		payload := toolResultPayloadToStream(*p, raw)
		return []agentsession.Event{protocolPayloadEvent(event, agentsession.KindToolCallResult, payload)}
	case agentsession.KindModelResponse:
		p, err := agentsession.Unmarshal[agentsession.ModelResponsePayload](&event)
		if err != nil {
			return nil
		}
		return []agentsession.Event{protocolPayloadEvent(event, agentsession.KindUsage, agentsession.UsagePayload{
			Input:           p.Usage.Input,
			Output:          p.Usage.Output,
			CacheRead:       p.Usage.CacheRead,
			CacheWrite:      p.Usage.CacheWrite,
			CostUSD:         p.Usage.CostUSD,
			CostUnavailable: p.Usage.CostUnavailable,
		})}
	case agentsession.KindReasoningSanitized:
		payload := agentsession.ReasoningPayload{Removed: true, Redacted: true}
		if raw {
			if p, err := agentsession.Unmarshal[agentsession.ReasoningSanitizedPayload](&event); err == nil {
				payload.Text = p.Removed
				payload.Redacted = false
			}
		}
		return []agentsession.Event{protocolPayloadEvent(event, agentsession.KindReasoning, payload)}
	case agentsession.KindCompaction:
		return []agentsession.Event{normalizeProtocolEvent(event)}
	case agentsession.KindTerminal:
		var p agentsession.TerminalPayload
		if err := json.Unmarshal(event.Data, &p); err != nil {
			var rawPayload map[string]any
			if err := json.Unmarshal(event.Data, &rawPayload); err == nil {
				p.TerminalState = agentsession.TerminalState(protocolStringValue(rawPayload["terminal_state"]))
				p.ContainmentMode = protocolStringValue(rawPayload["containment_mode"])
			}
		}
		return []agentsession.Event{protocolPayloadEvent(event, agentsession.KindTerminal, p)}
	default:
		return nil
	}
}

func toolResultPayloadToStream(p agentsession.ToolResultPayload, raw bool) agentsession.ToolCallResultStreamPayload {
	payload := agentsession.ToolCallResultStreamPayload{
		ToolCallID:      p.ToolCallID,
		ToolName:        p.ToolName,
		IsError:         p.IsError,
		DurationMS:      p.DurationMS,
		OutputBytes:     p.OutputBytes,
		Truncated:       p.Truncated,
		ExitCode:        p.ExitCode,
		ErrorCode:       p.ErrorCode,
		Metadata:        p.Metadata,
		ContainmentMode: protocolStringValue(p.Metadata["containment_mode"]),
		ChangedFiles:    stringSliceValue(p.Metadata["changed_files"]),
	}
	if !raw {
		payload.Redacted = true
		payload.Redaction = map[string]any{"output": true}
	}
	return payload
}

func usagePayloadFromMessage(msg *agentsession.Message) agentsession.UsagePayload {
	payload := agentsession.UsagePayload{
		Input:           msg.Usage.Input,
		Output:          msg.Usage.Output,
		CacheRead:       msg.Usage.CacheRead,
		CacheWrite:      msg.Usage.CacheWrite,
		CostUnavailable: msg.Usage.CostUnavailable,
		Provider:        msg.Provider,
		Model:           msg.Model,
	}
	if msg.Usage.Cost != nil {
		payload.CostUSD = msg.Usage.Cost.Total
	}
	return payload
}

func storeEventToProtocol(event StoreEvent) agentsession.Event {
	payload := agentsession.LifecyclePayload{State: string(event.Type)}
	if len(event.Payload) > 0 {
		var values map[string]any
		if err := json.Unmarshal(event.Payload, &values); err == nil {
			payload.Status = protocolStringValue(values["status"])
			payload.PID = protocolIntValue(values["pid"])
			payload.PGID = protocolIntValue(values["pgid"])
			payload.Signal = protocolStringValue(values["signal"])
			payload.ExitCode = protocolIntValue(values["exit_code"])
			payload.Error = protocolStringValue(values["error"])
			payload.Metadata = values
		}
	}
	return agentsession.Event{
		Type:      agentsession.KindLifecycle,
		Timestamp: event.CreatedAt,
		Data:      agentsession.MarshalPayload(payload),
	}
}

func manifestTerminalProtocolEvent(m *Manifest) (agentsession.Event, bool) {
	status := deriveStatus(m)
	if !status.IsTerminal() {
		return agentsession.Event{}, false
	}
	state := agentsession.TerminalIncomplete
	switch status {
	case StatusCompleted:
		state = agentsession.TerminalCompleted
	case StatusStopped, StatusStale:
		state = agentsession.TerminalInterrupted
	case StatusFailed:
		state = agentsession.TerminalProviderFailed
	}
	payload := agentsession.TerminalPayload{TerminalState: state}
	if last := m.LastEpoch(); last != nil {
		if last.Error != nil {
			payload.Error = *last.Error
		}
		if last.ErrorCode != nil {
			switch *last.ErrorCode {
			case "runtime_budget_exhausted":
				payload.TerminalState = agentsession.TerminalExhausted
			case "stopped":
				payload.TerminalState = agentsession.TerminalInterrupted
			}
		}
	}
	return agentsession.Event{
		Type: agentsession.KindTerminal,
		Data: agentsession.MarshalPayload(payload),
	}, true
}

func hasTerminalProtocolEvent(events []agentsession.Event) bool {
	for _, event := range events {
		if event.Type == agentsession.KindTerminal {
			return true
		}
	}
	return false
}

func protocolPayloadEvent(base agentsession.Event, typ string, payload any) agentsession.Event {
	return normalizeProtocolEvent(agentsession.Event{
		Type:      typ,
		ID:        base.ID,
		Timestamp: base.Timestamp,
		CWD:       base.CWD,
		Runtime:   base.Runtime,
		Data:      agentsession.MarshalPayload(payload),
	})
}

func normalizeProtocolEvent(event agentsession.Event) agentsession.Event {
	event.Schema = agentsession.Schema
	event.SchemaVersion = agentsession.SchemaVersion
	if event.Version == 0 {
		event.Version = agentsession.SchemaVersion
	}
	return event
}

func messageText(msg *agentsession.Message) string {
	if msg == nil {
		return ""
	}
	var parts []string
	for _, content := range msg.Content {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "")
}

func truncatePreview(text string, max int) string {
	if len(text) <= max {
		return text
	}
	if max <= 0 {
		return ""
	}
	return text[:max] + "\n... truncated ..."
}

func protocolStringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func stringSliceValue(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func protocolIntValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}
