package agent

import (
	"context"

	"custom-agent/gateway"
	"custom-agent/tools"

	"github.com/sashabaranov/go-openai"
)

// injectMainAgentToolArgs injects platform/user/chat into JSON tool arguments when required.
func injectMainAgentToolArgs(name, args string, msg gateway.IncomingMessage) string {
	out := args
	switch {
	case name == "save_memory" || name == "read_memory":
		if injected, err := tools.InjectMemoryArgs(out, msg.Platform, msg.UserID); err == nil {
			out = injected
		}
	case name == "create_scheduled_reminder" || name == "list_reminders" || name == "delete_reminder":
		if injected, err := tools.InjectReminderArgs(out, msg.Platform, msg.UserID, msg.ChatID); err == nil {
			out = injected
		}
	case name == "wallet_execute_transfer" || name == "wallet_execute_contract_call":
		if injected, err := tools.InjectWalletArgs(out, msg.Platform, msg.UserID, msg.ChatID); err == nil {
			out = injected
		}
	}
	return out
}

// runMainAgentSingleTool runs one tool (spawn_subagents or main-agent tools), optional failure-analyzer hint.
// Sets *failureAnalyzerUsed when the failure-analyzer path is taken (even if hint is empty).
func (a *Agent) runMainAgentSingleTool(ctx context.Context, msg gateway.IncomingMessage, name, args string, failureAnalyzerUsed *bool) (result string, analyzerHint string, walletInvoked bool) {
	if name == "wallet_execute_transfer" || name == "wallet_execute_contract_call" {
		walletInvoked = true
	}
	if name == "spawn_subagents" {
		result = a.handleSpawnSubagents(ctx, args, msg)
		return result, "", walletInvoked
	}
	args = injectMainAgentToolArgs(name, args, msg)
	var execErr error
	var corrID string
	result, execErr, corrID, _ = a.runMainAgentTool(msg, name, args)
	failed := execErr != nil || isErrorResult(result)
	if !failed {
		return result, "", walletInvoked
	}
	if a.failureAnalyzerEnabled && !*failureAnalyzerUsed && !a.disableSubagents && shouldRunFailureAnalyzer(name, execErr, result) {
		var hint string
		if h := a.runFailureAnalyzer(ctx, msg, name, args, execErr, result, corrID); h != "" {
			hint = h
		}
		*failureAnalyzerUsed = true
		return result, hint, walletInvoked
	}
	return result, "", walletInvoked
}

// buildMainAgentChatCompletionRequest prepares the request for one LLM round (OpenAI vs x402/Anthropic message shape).
func (a *Agent) buildMainAgentChatCompletionRequest(messages []openai.ChatCompletionMessage, toolDefs []openai.Tool) openai.ChatCompletionRequest {
	sendMessages := messages
	if a.skipCompaction {
		sendMessages = convertToMessagesAPIFormat(messages)
	}
	req := openai.ChatCompletionRequest{
		Model:    a.parentModel,
		Messages: sendMessages,
		Tools:    toolDefs,
	}
	if a.skipCompaction {
		req.MaxTokens = 4096 // x402 router requires max_tokens
	}
	return req
}

// structuredToolRoundMessages builds tool messages then system hints (same order as pre-refactor loop).
func (a *Agent) structuredToolRoundMessages(ctx context.Context, msg gateway.IncomingMessage, toolCalls []openai.ToolCall, failureAnalyzerUsed *bool, walletToolUsed *bool) []openai.ChatCompletionMessage {
	var out []openai.ChatCompletionMessage
	var hints []string
	for _, tc := range toolCalls {
		res, hint, w := a.runMainAgentSingleTool(ctx, msg, tc.Function.Name, tc.Function.Arguments, failureAnalyzerUsed)
		if w {
			*walletToolUsed = true
		}
		out = append(out, openai.ChatCompletionMessage{
			Role:       openai.ChatMessageRoleTool,
			Content:    res,
			ToolCallID: tc.ID,
		})
		if hint != "" {
			hints = append(hints, hint)
		}
	}
	for _, h := range hints {
		out = append(out, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: h,
		})
	}
	return out
}

// fallbackToolRoundMessages builds tool + hint messages for a single parsed tool call from assistant text.
func (a *Agent) fallbackToolRoundMessages(ctx context.Context, msg gateway.IncomingMessage, toolName, toolArgs string, failureAnalyzerUsed *bool, walletToolUsed *bool) []openai.ChatCompletionMessage {
	res, hint, w := a.runMainAgentSingleTool(ctx, msg, toolName, toolArgs, failureAnalyzerUsed)
	if w {
		*walletToolUsed = true
	}
	out := []openai.ChatCompletionMessage{
		{
			Role:       openai.ChatMessageRoleTool,
			Content:    res,
			ToolCallID: "fallback",
			Name:       toolName,
		},
	}
	if hint != "" {
		out = append(out, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: hint,
		})
	}
	return out
}
