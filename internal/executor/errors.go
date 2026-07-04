package executor

import "fmt"

type executorErrorCode string

const (
	errConfig                 executorErrorCode = "config"
	errProviderRateLimited    executorErrorCode = "provider_rate_limited"
	errProviderTimeout        executorErrorCode = "provider_timeout"
	errProviderUnavailable    executorErrorCode = "provider_unavailable"
	errProviderInvalidRequest executorErrorCode = "provider_invalid_request"
	errProviderContextLimit   executorErrorCode = "provider_context_limit"
	errToolTimeout            executorErrorCode = "tool_timeout"
	errToolInfra              executorErrorCode = "tool_infra"
	errToolPolicyDenied       executorErrorCode = "tool_policy_denied"
	errAgentProtocol          executorErrorCode = "agent_protocol"
	errAgentIncomplete        executorErrorCode = "agent_incomplete"
	errRuntimeBudgetExhausted executorErrorCode = "runtime_budget_exhausted"
	errStopped                executorErrorCode = "stopped"
)

type executorError struct {
	Code       executorErrorCode
	Message    string
	Retryable  bool
	StatusCode int
}

func (e *executorError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s:%s", e.Code, e.Message)
}

func newExecutorError(code executorErrorCode, message string) *executorError {
	return &executorError{Code: code, Message: message}
}

func retryableExecutorError(code executorErrorCode, message string) *executorError {
	return &executorError{Code: code, Message: message, Retryable: true}
}
