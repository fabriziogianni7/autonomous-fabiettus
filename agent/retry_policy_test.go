package agent

import (
	"errors"
	"testing"
)

func TestIsErrorResult(t *testing.T) {
	if !isErrorResult("Error: boom") {
		t.Fatal("expected Error prefix")
	}
	if isErrorResult("ok") {
		t.Fatal("expected non-error")
	}
}

func TestShouldAutoRetryExecuteTool(t *testing.T) {
	if !shouldAutoRetryExecuteTool("http_request", nil, "Error: connection timeout") {
		t.Fatal("expected transient error_result retry")
	}
	if shouldAutoRetryExecuteTool("save_memory", nil, "Error: connection timeout") {
		t.Fatal("save_memory should not auto-retry on timeout message")
	}
	if !shouldAutoRetryExecuteTool("http_request", errors.New("context deadline exceeded"), "") {
		t.Fatal("expected exec err retry")
	}
}

func TestIsTerminalToolFailure(t *testing.T) {
	if !isTerminalToolFailure("x", errors.New("unknown tool: foo"), "") {
		t.Fatal("unknown tool terminal")
	}
	if !isTerminalToolFailure("x", errors.New("invalid arguments: bad json"), "") {
		t.Fatal("invalid args terminal")
	}
	if !isTerminalToolFailure("x", nil, "Error: sub-agents cannot use this tool.") {
		t.Fatal("subagent denial terminal")
	}
	if isTerminalToolFailure("http_request", nil, "Error: timeout") {
		t.Fatal("timeout should not be terminal")
	}
}

func TestShouldRunFailureAnalyzer(t *testing.T) {
	if !shouldRunFailureAnalyzer("lifi_get_quote", nil, "Error: no route") {
		t.Fatal("expected analyzer for non-terminal error_result")
	}
	if shouldRunFailureAnalyzer("x", errors.New("unknown tool: z"), "") {
		t.Fatal("terminal should skip analyzer")
	}
}
