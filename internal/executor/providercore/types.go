package providercore

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderGemini    Provider = "gemini"
	ProviderCodex     Provider = "codex"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolDefinition
	MaxOutputTokens int
	ReasoningEffort string
	PreviousID      string
	Store           bool
	Stream          bool
}

type EventType string

const (
	EventTextDelta     EventType = "text_delta"
	EventTextFinal     EventType = "text_final"
	EventReasoning     EventType = "reasoning"
	EventToolCallStart EventType = "tool_call_start"
	EventToolCallDelta EventType = "tool_call_delta"
	EventToolCallEnd   EventType = "tool_call_end"
	EventUsage         EventType = "usage"
	EventDone          EventType = "done"
	EventError         EventType = "error"
)

type TerminalStatus string

const (
	StatusCompleted  TerminalStatus = "completed"
	StatusIncomplete TerminalStatus = "incomplete"
	StatusFailed     TerminalStatus = "failed"
	StatusCancelled  TerminalStatus = "cancelled"
	StatusPending    TerminalStatus = "pending"
)

type ErrorClass string

const (
	ErrorNone           ErrorClass = ""
	ErrorRateLimited    ErrorClass = "rate_limited"
	ErrorTimeout        ErrorClass = "timeout"
	ErrorUnavailable    ErrorClass = "unavailable"
	ErrorInvalidRequest ErrorClass = "invalid_request"
	ErrorContextLimit   ErrorClass = "context_limit"
	ErrorCancelled      ErrorClass = "cancelled"
)

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type Usage struct {
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
	ReasoningTokens   int
	CostUSD           float64
	CostKnown         bool
}

type Event struct {
	Type              EventType
	Text              string
	Reasoning         string
	ToolCallID        string
	ToolName          string
	ArgumentsFragment string
	Usage             Usage
	Status            TerminalStatus
	StopReason        string
	Error             *Error
	ResponseID        string
	AsyncJobID        string
}

type Error struct {
	Class      ErrorClass
	Message    string
	Retryable  bool
	StatusCode int
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Class)
	}
	if e.Class == "" {
		return e.Message
	}
	return fmt.Sprintf("%s:%s", e.Class, e.Message)
}

type Response struct {
	ID         string
	AsyncJobID string
	Text       string
	ToolCalls  []ToolCall
	Usage      Usage
	Status     TerminalStatus
	StopReason string
	Error      *Error
	Events     []Event
}

type Adapter interface {
	Complete(ctx context.Context, request Request, sequence int) (Response, error)
}

func Classify(statusCode int, message string) *Error {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case IsContextLimitMessage(lower):
		return &Error{Class: ErrorContextLimit, Message: message, StatusCode: statusCode}
	case statusCode == http.StatusTooManyRequests:
		return &Error{Class: ErrorRateLimited, Message: message, Retryable: true, StatusCode: statusCode}
	case statusCode == http.StatusRequestTimeout || strings.Contains(lower, "timeout"):
		return &Error{Class: ErrorTimeout, Message: message, Retryable: true, StatusCode: statusCode}
	case statusCode >= 500:
		return &Error{Class: ErrorUnavailable, Message: message, Retryable: true, StatusCode: statusCode}
	case statusCode >= 400:
		return &Error{Class: ErrorInvalidRequest, Message: message, StatusCode: statusCode}
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many request"):
		return &Error{Class: ErrorRateLimited, Message: message, Retryable: true, StatusCode: statusCode}
	default:
		return &Error{Class: ErrorUnavailable, Message: message, Retryable: true, StatusCode: statusCode}
	}
}

func IsContextLimitMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	needles := []string{
		"context_length_exceeded",
		"context length exceeded",
		"maximum context length",
		"maximum context",
		"context window",
		"prompt is too long",
		"prompt too long",
		"string too long",
		"exceeds the maximum number of tokens",
		"exceeded token limit",
		"token limit exceeded",
		"too many tokens",
		"input is too long",
	}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	if strings.Contains(lower, "context") && strings.Contains(lower, "limit") {
		return true
	}
	if strings.Contains(lower, "token") && (strings.Contains(lower, "limit") || strings.Contains(lower, "maximum") || strings.Contains(lower, "exceed")) {
		return true
	}
	return false
}
