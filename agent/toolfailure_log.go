package agent

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"custom-agent/gateway"
	"custom-agent/toolfailure"
)

func newCorrelationID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func failureMessage(execErr error, result string) string {
	if execErr != nil {
		return execErr.Error()
	}
	return result
}

func (a *Agent) recordFailure(e toolfailure.Entry) {
	if a.failureStore == nil {
		return
	}
	a.failureStore.Record(e)
}

func (a *Agent) recordToolOutcome(msg gateway.IncomingMessage, source, tool, argsJSON string, attempt int, execErr error, result string, correlationID string, subRole string, subIdx int) {
	if a.failureStore == nil {
		return
	}
	kind := failureKindFromOutcome(execErr, result)
	if kind == "" {
		return
	}
	tk := toolfailure.KindExecError
	if kind == "error_result" {
		tk = toolfailure.KindErrorResult
	}
	a.recordFailure(toolfailure.Entry{
		CorrelationID:   correlationID,
		Tool:            tool,
		ArgsSummary:     argsJSON,
		Kind:            tk,
		Message:         failureMessage(execErr, result),
		Platform:        msg.Platform,
		UserID:          msg.UserID,
		ChatID:          msg.ChatID,
		Source:          source,
		Attempt:         attempt,
		SubagentRole:    subRole,
		SubagentTaskIdx: subIdx,
	})
}

func (a *Agent) recordRecovery(correlationID, tool, source, note string, msg gateway.IncomingMessage) {
	if a.failureStore == nil || correlationID == "" {
		return
	}
	a.recordFailure(toolfailure.Entry{
		CorrelationID:  correlationID,
		Tool:           tool,
		Kind:           toolfailure.KindRecovery,
		Message:        note,
		Platform:       msg.Platform,
		UserID:         msg.UserID,
		ChatID:         msg.ChatID,
		Source:         source,
		Resolved:       true,
		ResolutionNote: note,
	})
}

func (a *Agent) recordSubagentDenied(msg gateway.IncomingMessage, tool, argsJSON string, subRole string, subIdx int) {
	if a.failureStore == nil {
		return
	}
	a.recordFailure(toolfailure.Entry{
		Tool:            tool,
		ArgsSummary:     argsJSON,
		Kind:            toolfailure.KindSubagentDenied,
		Message:         "sub-agents cannot use this tool",
		Platform:        msg.Platform,
		UserID:          msg.UserID,
		ChatID:          msg.ChatID,
		Source:          "subagent",
		SubagentRole:    subRole,
		SubagentTaskIdx: subIdx,
	})
}

func (a *Agent) recordSpawnSubagentsFailure(msg gateway.IncomingMessage, message string) {
	if a.failureStore == nil {
		return
	}
	a.recordFailure(toolfailure.Entry{
		Tool:     "spawn_subagents",
		Kind:     toolfailure.KindSpawnSubagentsError,
		Message:  message,
		Platform: msg.Platform,
		UserID:   msg.UserID,
		ChatID:   msg.ChatID,
		Source:   "spawn_subagents",
	})
}

// runMainAgentTool executes a tool with optional one-shot auto-retry for transient errors, logging failures.
func (a *Agent) runMainAgentTool(msg gateway.IncomingMessage, toolName, argsJSON string) (result string, lastErr error, correlationID string, autoRetried bool) {
	correlationID = newCorrelationID()
	attempt := 1
	result, lastErr = a.tools.ExecuteTool(toolName, argsJSON)
	if lastErr != nil {
		result = "Error: " + lastErr.Error()
	}
	if lastErr != nil || isErrorResult(result) {
		a.recordToolOutcome(msg, "main_agent", toolName, argsJSON, attempt, lastErr, result, correlationID, "", -1)
	}
	if shouldAutoRetryExecuteTool(toolName, lastErr, result) {
		autoRetried = true
		attempt++
		// Clear in-band error state for second attempt
		var err2 error
		res2, err2 := a.tools.ExecuteTool(toolName, argsJSON)
		if err2 != nil {
			res2 = "Error: " + err2.Error()
		}
		if err2 == nil && !isErrorResult(res2) {
			a.recordRecovery(correlationID, toolName, "main_agent", "succeeded after auto-retry", msg)
			return res2, nil, correlationID, true
		}
		a.recordToolOutcome(msg, "main_agent", toolName, argsJSON, attempt, err2, res2, correlationID, "", -1)
		return res2, err2, correlationID, true
	}
	return result, lastErr, correlationID, false
}

func isFailureAnalyzerRole(role string) bool {
	r := strings.ToLower(strings.TrimSpace(role))
	return r == "failure-analyzer" || r == "analyzer"
}
