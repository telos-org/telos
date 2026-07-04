package providercore

import (
	"context"
	"fmt"
	"strings"
)

type Collector struct {
	calls             map[string]*ToolCall
	order             []string
	openEmptyID       []string
	pendingEmptyDelta string
	synthetic         int
	text              strings.Builder
	usage             Usage
	usageSeen         bool
	events            []Event
	responseID        string
	asyncJobID        string
	status            TerminalStatus
	stopReason        string
	err               *Error
}

func NewCollector() *Collector {
	return &Collector{calls: map[string]*ToolCall{}}
}

func Collect(ctx context.Context, events <-chan Event) Response {
	collector := NewCollector()
	for {
		select {
		case <-ctx.Done():
			collector.Apply(Event{Type: EventError, Error: &Error{Class: ErrorCancelled, Message: ctx.Err().Error()}})
			return collector.Response()
		case event, ok := <-events:
			if !ok {
				return collector.Response()
			}
			collector.Apply(event)
			if event.Type == EventDone || event.Type == EventError {
				return collector.Response()
			}
		}
	}
}

func (c *Collector) Apply(event Event) {
	c.events = append(c.events, event)
	if event.ResponseID != "" {
		c.responseID = event.ResponseID
	}
	if event.AsyncJobID != "" {
		c.asyncJobID = event.AsyncJobID
	}
	if event.Status != "" {
		c.status = event.Status
	}
	if event.StopReason != "" {
		c.stopReason = event.StopReason
	}
	switch event.Type {
	case EventTextDelta:
		c.text.WriteString(event.Text)
	case EventTextFinal:
		if event.Text != "" {
			c.text.Reset()
			c.text.WriteString(event.Text)
		}
	case EventToolCallStart:
		c.start(event.ToolCallID, event.ToolName)
	case EventToolCallDelta:
		c.delta(event.ToolCallID, event.ArgumentsFragment)
	case EventToolCallEnd:
		c.end(event.ToolCallID)
	case EventUsage:
		c.usage = mergeUsage(c.usage, event.Usage)
		c.usageSeen = true
	case EventError:
		c.err = event.Error
	case EventDone:
		if c.status == "" {
			c.status = StatusCompleted
		}
	}
}

func (c *Collector) Response() Response {
	var calls []ToolCall
	for _, key := range c.order {
		call := c.calls[key]
		if call == nil || call.Name == "" {
			continue
		}
		if strings.TrimSpace(call.ID) == "" {
			call.ID = fmt.Sprintf("call_%d", len(calls)+1)
		}
		calls = append(calls, *call)
	}
	return Response{
		ID:         c.responseID,
		AsyncJobID: c.asyncJobID,
		Text:       c.text.String(),
		ToolCalls:  calls,
		Usage:      c.usage,
		Status:     c.status,
		StopReason: c.stopReason,
		Error:      c.err,
		Events:     append([]Event(nil), c.events...),
	}
}

func (c *Collector) Err() *Error {
	return c.err
}

func (c *Collector) start(id, name string) {
	key := id
	if id == "" {
		if c.pendingEmptyDelta != "" {
			key = c.pendingEmptyDelta
			c.pendingEmptyDelta = ""
		} else {
			c.synthetic++
			key = fmt.Sprintf("\x00synthetic-%d", c.synthetic)
			c.openEmptyID = append(c.openEmptyID, key)
		}
	}
	call := c.ensure(key, id)
	if name != "" && call.Name == "" {
		call.Name = name
	}
}

func (c *Collector) delta(id, fragment string) {
	key, ok := c.resolveKey(id)
	if !ok {
		if id == "" {
			c.synthetic++
			key = fmt.Sprintf("\x00synthetic-%d", c.synthetic)
			c.openEmptyID = append(c.openEmptyID, key)
			c.pendingEmptyDelta = key
		} else {
			key = id
		}
		c.ensure(key, id)
	}
	c.calls[key].Arguments += fragment
}

func (c *Collector) end(id string) {
	if id == "" && len(c.openEmptyID) > 0 {
		closed := c.openEmptyID[len(c.openEmptyID)-1]
		c.openEmptyID = c.openEmptyID[:len(c.openEmptyID)-1]
		if closed == c.pendingEmptyDelta {
			c.pendingEmptyDelta = ""
		}
	}
}

func (c *Collector) ensure(key, id string) *ToolCall {
	if call := c.calls[key]; call != nil {
		return call
	}
	call := &ToolCall{ID: id}
	c.calls[key] = call
	c.order = append(c.order, key)
	return call
}

func (c *Collector) resolveKey(id string) (string, bool) {
	if id == "" {
		if len(c.openEmptyID) == 0 {
			return "", false
		}
		return c.openEmptyID[len(c.openEmptyID)-1], true
	}
	if _, ok := c.calls[id]; ok {
		return id, true
	}
	return "", false
}

func mergeUsage(left, right Usage) Usage {
	if right.InputTokens != 0 {
		left.InputTokens = right.InputTokens
	}
	if right.OutputTokens != 0 {
		left.OutputTokens = right.OutputTokens
	}
	if right.CachedInputTokens != 0 {
		left.CachedInputTokens = right.CachedInputTokens
	}
	if right.ReasoningTokens != 0 {
		left.ReasoningTokens = right.ReasoningTokens
	}
	if right.CostKnown {
		left.CostUSD = right.CostUSD
		left.CostKnown = true
	}
	return left
}
