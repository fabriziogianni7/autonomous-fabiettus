package agent

import (
	"strings"
)

// isErrorResult is true when the tool returned success but content is an in-band error.
func isErrorResult(result string) bool {
	s := strings.TrimSpace(result)
	return strings.HasPrefix(s, "Error:") || strings.HasPrefix(s, "error:")
}

// failureKindFromOutcome returns toolfailure kind for logging (exec_error vs error_result).
func failureKindFromOutcome(err error, result string) string {
	if err != nil {
		return "exec_error"
	}
	if isErrorResult(result) {
		return "error_result"
	}
	return ""
}

// shouldAutoRetryExecuteTool is true for one automatic re-invocation of the same tool+args (transient I/O).
func shouldAutoRetryExecuteTool(tool string, err error, result string) bool {
	if err != nil {
		s := strings.ToLower(err.Error())
		return transientExecError(s)
	}
	if !isErrorResult(result) {
		return false
	}
	s := strings.ToLower(result)
	switch tool {
	case "http_request", "web_search", "lifi_get_quote", "lifi_track_status", "lifi_check_route", "lifi_get_token",
		"wallet_get_balance", "wallet_list_transactions", "wallet_get_portfolio", "wallet_get_portfolio_value", "wallet_get_activity":
		return transientMessage(s)
	default:
		return false
	}
}

func transientExecError(s string) bool {
	return strings.Contains(s, "timeout") ||
		strings.Contains(s, "deadline exceeded") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "eof") ||
		strings.Contains(s, "temporary failure") ||
		strings.Contains(s, "i/o timeout")
}

func transientMessage(s string) bool {
	return strings.Contains(s, "timeout") ||
		strings.Contains(s, "timed out") ||
		strings.Contains(s, "429") ||
		strings.Contains(s, "503") ||
		strings.Contains(s, "502") ||
		strings.Contains(s, "504") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "temporary") ||
		strings.Contains(s, "try again")
}

// isTerminalToolFailure is true when an automatic retry or failure-analyzer would not help or is unsafe.
func isTerminalToolFailure(tool string, err error, result string) bool {
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "unknown tool") {
			return true
		}
		if strings.Contains(msg, "invalid arguments") {
			return true
		}
		return false
	}
	if !isErrorResult(result) {
		return false
	}
	low := strings.ToLower(result)
	if strings.Contains(low, "sub-agents cannot use this tool") {
		return true
	}
	if strings.Contains(low, "wallet not configured") {
		return true
	}
	if strings.Contains(low, "missing user context") {
		return true
	}
	if strings.Contains(low, "approval required") {
		return true
	}
	if strings.Contains(low, "forbidden") && strings.Contains(low, "tool") {
		return true
	}
	_ = tool
	return false
}

// shouldRunFailureAnalyzer after a failed tool outcome: non-terminal failures may get one analyzer pass.
func shouldRunFailureAnalyzer(tool string, err error, result string) bool {
	if err == nil && !isErrorResult(result) {
		return false
	}
	return !isTerminalToolFailure(tool, err, result)
}
