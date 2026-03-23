package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"custom-agent/compaction"
	"custom-agent/conversation"
	"custom-agent/gateway"
	"custom-agent/memory"
	"custom-agent/session"
	"custom-agent/skills"
	"custom-agent/spend"
	"custom-agent/toolfailure"
	"custom-agent/tools"
	"custom-agent/wallet/redact"

	"github.com/sashabaranov/go-openai"
)

const (
	agentParentModel = "openai/gpt-oss-120b" // default when parentModel not specified
	maxToolRounds    = 20
)

// subagentModels are rotated per sub-agent index to spread load across Groq's per-model TPM quotas.
var subagentModels = []string{
	"openai/gpt-oss-120b",
}

func subagentModelForIndex(idx int) string {
	if len(subagentModels) == 0 {
		return "openai/gpt-oss-120b"
	}
	return subagentModels[idx%len(subagentModels)]
}

// Agent processes messages and returns replies using an LLM.
type Agent struct {
	client                  *openai.Client
	parentModel             string                   // model for chat completion; when empty, use default
	subagentModel           string                   // model for subagents; when empty, use subagentModelForIndex (Groq rotation)
	subagentModelForRole    func(role string) string // optional; when set and role non-empty, overrides subagentModel
	subagentTimeoutSec      int                      // default 60; used for non-quant subagents
	subagentQuantTimeoutSec int                      // default 90; used when role=quant
	disableSubagents        bool                     // when true, spawn_subagents returns "do it yourself" (no subagent)
	systemPrompt            string
	compactor               *compaction.Compactor
	skipCompaction          bool // when true, bypass compaction (e.g. autonomous mode)
	tools                   *tools.Tools
	memoryStore             *memory.Store
	convStore               *conversation.Store
	skillsDir               string
	skillsMgr               *skills.Manager
	spendStore              *spend.Store
	failureStore            *toolfailure.Store
	dataRoot                string
	failureAnalyzerEnabled  bool
}

// New creates an Agent with the given LLM client, system prompt, tools, and optional stores.
// parentModel: model for chat completion; when empty, use default (Groq-specific).
// subagentModel: model for subagents; when empty, use Groq rotation.
// tokenThreshold: when context exceeds this (approx tokens), compaction is triggered. 0 = default (4000).
// skipCompaction: when true, bypass compaction entirely (e.g. autonomous mode).
// skillsDir: optional path to skills directory; when set, skill descriptions are injected into system prompt.
// modelForRole: optional; when non-nil and spawn_subagents uses role, returns model for that role (autonomous mode).
// subagentTimeoutSec, subagentQuantTimeoutSec: timeouts for subagents; 0 = use package defaults (60, 90).
// disableSubagents: when true, spawn_subagents returns instructions to do the work inline (no subagent).
// failureStore: optional JSONL tool-failure log under DataRoot; dataRoot used for COMMON_ISSUES.md; failureAnalyzer enables one analyzer pass per message when FAILURE_ANALYZER=1.
func New(client *openai.Client, parentModel string, subagentModel string, systemPrompt string, tokenThreshold int, skipCompaction bool, toolSet *tools.Tools, convStore *conversation.Store, skillsDir string, modelForRole func(role string) string, spendStore *spend.Store, subagentTimeoutSec, subagentQuantTimeoutSec int, disableSubagents bool, failureStore *toolfailure.Store, dataRoot string, failureAnalyzerEnabled bool) *Agent {
	if parentModel == "" {
		parentModel = agentParentModel
	}
	var skillsMgr *skills.Manager
	if skillsDir != "" {
		skillsMgr = skills.NewManager(skillsDir)
	}
	subTimeout := subagentTimeoutSec
	if subTimeout <= 0 {
		subTimeout = 60
	}
	quantTimeout := subagentQuantTimeoutSec
	if quantTimeout <= 0 {
		quantTimeout = 90
	}
	return &Agent{
		client:                  client,
		parentModel:             parentModel,
		subagentModel:           subagentModel,
		subagentModelForRole:    modelForRole,
		subagentTimeoutSec:      subTimeout,
		subagentQuantTimeoutSec: quantTimeout,
		disableSubagents:        disableSubagents,
		systemPrompt:            systemPrompt,
		compactor:               compaction.NewCompactor(client, parentModel, tokenThreshold, spendStore),
		skipCompaction:          skipCompaction,
		tools:                   toolSet,
		memoryStore:             toolSet.MemoryStore,
		convStore:               convStore,
		skillsDir:               skillsDir,
		skillsMgr:               skillsMgr,
		spendStore:              spendStore,
		failureStore:            failureStore,
		dataRoot:                dataRoot,
		failureAnalyzerEnabled:  failureAnalyzerEnabled,
	}
}

// HandleMessage processes an incoming message and returns the reply.
func (a *Agent) HandleMessage(ctx context.Context, msg gateway.IncomingMessage) string {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return "Hello! Send me a message and I'll respond."
	}

	// Handle newSkill command (interactive flow)
	if handled, res := a.handleNewSkill(ctx, text, msg.Platform, msg.UserID); handled {
		return res
	}

	// Handle /new command
	if text == "/new" {
		if err := session.Clear(msg.Platform, msg.UserID); err != nil {
			return "Failed to clear session: " + err.Error()
		}
		_ = conversation.Clear(msg.Platform, msg.UserID)
		return "Session cleared. Starting fresh!"
	}

	// Proactive save when user explicitly says "remember" or "memorize"
	if a.memoryStore != nil {
		if content := extractRememberContent(text); content != "" {
			if err := a.memoryStore.Save(msg.Platform, msg.UserID, content, ""); err != nil {
				log.Printf("[agent] proactive save_memory failed: %v", err)
			} else {
				log.Printf("[agent] proactively saved memory: %s", redact.Redact(content))
			}
		}
	}

	// Handle approval messages
	if cmd, ok := tools.ParseApprovalMessage(text); ok {
		if handled, result := a.tools.TryWalletApproval(ctx, cmd, msg.Platform, msg.UserID, msg.ChatID); handled {
			return result
		}
		if err := tools.ApproveCommand(cmd); err != nil {
			return "Approval failed: " + err.Error()
		}
		return "Approved."
	}

	history, err := session.Load(msg.Platform, msg.UserID)
	if err != nil {
		// log handled by caller if needed
	}

	// Threshold-based compaction: summarize old context when it exceeds limit (skipped in autonomous mode)
	var summaryBlock string
	var recent []session.Message
	if a.skipCompaction {
		recent = session.Recent(history)
	} else {
		summaryBlock, recent, _ = a.compactor.CompactIfNeeded(ctx, history, a.systemPrompt)
		if recent == nil {
			recent = session.Recent(history)
		}
	}

	// Retrieve relevant long-term memories and past conversation (embedding-based if available)
	var contextBlocks []string
	if a.memoryStore != nil {
		if mems, err := a.memoryStore.Search(msg.Platform, msg.UserID, text, 5); err == nil && len(mems) > 0 {
			var b strings.Builder
			b.WriteString("--- Relevant memories ---\n")
			for _, m := range mems {
				b.WriteString("- ")
				b.WriteString(m.Content)
				if m.Tags != "" {
					b.WriteString(" [")
					b.WriteString(m.Tags)
					b.WriteString("]")
				}
				b.WriteString("\n")
			}
			b.WriteString("--- End memories ---")
			contextBlocks = append(contextBlocks, b.String())
		}
	}
	if a.convStore != nil {
		if entries, err := a.convStore.Search(msg.Platform, msg.UserID, text, 5); err == nil && len(entries) > 0 {
			contextBlocks = append(contextBlocks, conversation.FormatEntries(entries))
		}
	}

	systemContent := a.systemPrompt
	if a.skillsMgr != nil {
		if block := a.buildSkillsDescriptionBlock(); block != "" {
			systemContent += "\n\n" + block
		}
	}
	messages := make([]openai.ChatCompletionMessage, 0, len(recent)+5)
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: systemContent,
	})
	// When user wants to send and we have prior context, inject reminder so LLM doesn't repeat prior "Done!" without calling the tool
	if a.tools.Wallet != nil && len(recent) > 0 && wantsWalletSend(text) {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: "CRITICAL: The user is asking to send funds. You MUST call wallet_execute_transfer now. Do NOT respond with text claiming you sent—only a tool call actually executes. Reply with a tool call.",
		})
	}
	for _, block := range contextBlocks {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: block,
		})
	}
	if summaryBlock != "" {
		log.Printf("[agent] context compacted: %d history messages → summary + %d recent", len(history), len(recent))
		log.Printf("[agent] compacted summary:\n%s", redact.Redact(summaryBlock))
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: summaryBlock,
		})
	}
	for _, m := range recent {
		role := openai.ChatMessageRoleUser
		if m.Role == "assistant" {
			role = openai.ChatMessageRoleAssistant
		}
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    role,
			Content: m.Content,
		})
	}
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: text,
	})

	toolDefs := tools.Definitions()
	mustExecuteWallet := a.tools.Wallet != nil && wantsWalletSend(text)
	walletToolUsed := false
	failureAnalyzerUsed := false

	for i := 0; i < maxToolRounds; i++ {
		req := a.buildMainAgentChatCompletionRequest(messages, toolDefs)
		resp, err := createChatCompletionWithRetry(ctx, a.client, req)
		if err != nil {
			log.Printf("[agent] LLM error: %v", err)
			return "Sorry, I couldn't process that. Please try again."
		}

		if a.spendStore != nil && resp.Usage.TotalTokens > 0 {
			a.spendStore.Record(resp.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
		}

		if len(resp.Choices) == 0 {
			log.Printf("[agent] LLM returned empty choices (model=%s)", a.parentModel)
			return "I didn't get a response. Try again?"
		}

		msgResp := resp.Choices[0].Message

		if len(msgResp.ToolCalls) > 0 {
			messages = append(messages, msgResp)
			messages = append(messages, a.structuredToolRoundMessages(ctx, msg, msgResp.ToolCalls, &failureAnalyzerUsed, &walletToolUsed)...)
			continue
		}

		if toolName, toolArgs, ok := tools.ParseToolCallFromContent(msgResp.Content); ok {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: msgResp.Content,
			})
			messages = append(messages, a.fallbackToolRoundMessages(ctx, msg, toolName, toolArgs, &failureAnalyzerUsed, &walletToolUsed)...)
			continue
		}

		// Final text response
		reply := strings.TrimSpace(msgResp.Content)
		if reply == "" {
			log.Printf("[agent] LLM returned empty content (model=%s, hasToolCalls=%v)", a.parentModel, len(msgResp.ToolCalls) > 0)
			return "I didn't get a response. Try again?"
		}
		if mustExecuteWallet && !walletToolUsed && claimsWalletWasSent(reply) {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleSystem,
				Content: "You claimed a wallet transaction was sent, but no wallet execution tool was called in this turn. Do not claim success. Call wallet_execute_transfer or wallet_execute_contract_call, or ask a clarifying question if details are missing.",
			})
			continue
		}
		_ = session.Append(msg.Platform, msg.UserID, text, reply)
		if a.convStore != nil {
			_ = a.convStore.Add(msg.Platform, msg.UserID, "user", text)
			_ = a.convStore.Add(msg.Platform, msg.UserID, "assistant", reply)
		}
		return reply
	}

	return "I hit the tool limit. Please try a simpler request."
}

var (
	rememberPrefix   = regexp.MustCompile(`(?i)^(?:remember|memorize)\s*(?:that|:)?\s*`)
	rememberName     = regexp.MustCompile(`(?i)^(?:remember|memorize)\s+my\s+name\s+is\s+(.+)$`)
	walletSendIntent = regexp.MustCompile(`(?i)(send|transfer|pay|invio)\s+.*(0x[0-9a-fA-F]{40}|eth|wei|matic)|0x[0-9a-fA-F]{40}.*(send|transfer)`)
	walletSentClaim  = regexp.MustCompile(`(?i)(done!? i've sent|i've sent|i sent|transaction hash|hash:\s*0x|explorer:\s*https?://|sent\s+.*\s+to\s+0x[0-9a-fA-F]{40})`)
)

func wantsWalletSend(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) < 8 {
		return false
	}
	return walletSendIntent.MatchString(text)
}

func claimsWalletWasSent(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	return walletSentClaim.MatchString(text)
}

const maxSkillDescriptionsLen = 1500

func (a *Agent) buildSkillsDescriptionBlock() string {
	if a.skillsMgr == nil {
		return ""
	}
	list, err := a.skillsMgr.List()
	if err != nil || len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- Available skills (use list_skills / read_skill to inspect) ---\n")
	n := 0
	for _, s := range list {
		line := "- " + s.Name + ": " + s.Description + "\n"
		if b.Len()+len(line) > maxSkillDescriptionsLen {
			break
		}
		b.WriteString(line)
		n++
	}
	b.WriteString("--- End skills ---")
	return b.String()
}

// handleSpawnSubagents parses spawn_subagents args, runs sub-agents concurrently, and returns formatted results.
// When disableSubagents is true, returns instructions for the main agent to perform the analysis inline.
func (a *Agent) handleSpawnSubagents(ctx context.Context, argsJSON string, msg gateway.IncomingMessage) string {
	var args struct {
		Tasks []string `json:"tasks"`
		Role  string   `json:"role"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		a.recordSpawnSubagentsFailure(msg, err.Error())
		return "Error: invalid spawn_subagents arguments: " + err.Error()
	}
	if len(args.Tasks) == 0 {
		a.recordSpawnSubagentsFailure(msg, "tasks cannot be empty")
		return "Error: tasks cannot be empty."
	}
	if a.disableSubagents {
		var b strings.Builder
		b.WriteString("Subagents are disabled. Perform the analysis yourself in this turn.\n\n")
		b.WriteString("Tasks:\n")
		for i, t := range args.Tasks {
			t = strings.TrimSpace(t)
			if t != "" {
				b.WriteString(fmt.Sprintf("%d. %s\n", i+1, t))
			}
		}
		b.WriteString("\nUse STRATEGY.md: if you have `strategy_factor_analysis` output, treat it as the baseline for p, EV, Kelly, and size; align quant with that JSON. ")
		b.WriteString("Use the formulas in STRATEGY.md (EV, Kelly, position size, deployable base by trade_type). ")
		b.WriteString("Return: EV, Kelly fraction, recommended size USD, and go/no-go with one-line reasoning. ")
		b.WriteString("Then proceed to execute or skip based on your conclusion.")
		return b.String()
	}
	specs := make([]SubtaskSpec, len(args.Tasks))
	for i, t := range args.Tasks {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		specs[i] = SubtaskSpec{Task: t, Role: args.Role, Index: i}
	}
	// Filter out empty tasks
	n := 0
	for _, s := range specs {
		if s.Task != "" {
			specs[n] = s
			n++
		}
	}
	specs = specs[:n]
	if len(specs) == 0 {
		a.recordSpawnSubagentsFailure(msg, "no valid tasks provided")
		return "Error: no valid tasks provided."
	}
	role := strings.ToLower(strings.TrimSpace(args.Role))
	timeoutSec := a.subagentTimeoutSec
	if role == "quant" {
		timeoutSec = a.subagentQuantTimeoutSec
	}
	opts := &SubagentOpts{PerChildTimeout: time.Duration(timeoutSec) * time.Second}
	results, err := a.RunSubagents(ctx, specs, msg, opts)
	if err != nil {
		a.recordSpawnSubagentsFailure(msg, err.Error())
		return "Error running sub-agents: " + err.Error()
	}
	quantCorrID := newCorrelationID()
	// Retry once on quant timeout (subagent returned Err); log first wave for observability.
	if role == "quant" && hasSubagentError(results) {
		if a.failureStore != nil {
			a.recordFailure(toolfailure.Entry{
				CorrelationID: quantCorrID,
				Tool:          "spawn_subagents",
				Kind:          toolfailure.KindErrorResult,
				Message:       "quant subagent errors before retry",
				Platform:      msg.Platform,
				UserID:        msg.UserID,
				ChatID:        msg.ChatID,
				Source:        "spawn_subagents",
			})
		}
		log.Printf("[agent] quant subagent timeout/error, retrying once")
		results, err = a.RunSubagents(ctx, specs, msg, opts)
		if err != nil {
			a.recordSpawnSubagentsFailure(msg, err.Error())
			return "Error running sub-agents: " + err.Error()
		}
		if !hasSubagentError(results) {
			a.recordRecovery(quantCorrID, "spawn_subagents", "spawn_subagents", "quant subagents succeeded after retry", msg)
		}
	}
	return FormatSubagentResults(results)
}

// hasSubagentError returns true if any subagent result has an error.
func hasSubagentError(results []SubtaskResult) bool {
	for _, r := range results {
		if r.Err != nil {
			return true
		}
	}
	return false
}

// isTimeoutError returns true if the error indicates an HTTP or context timeout.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "Timeout exceeded") || strings.Contains(s, "request canceled")
}

// createChatCompletionWithRetry calls CreateChatCompletion and retries once on timeout.
func createChatCompletionWithRetry(ctx context.Context, client *openai.Client, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	resp, err := client.CreateChatCompletion(ctx, req)
	if err == nil {
		return resp, nil
	}
	if !isTimeoutError(err) {
		return openai.ChatCompletionResponse{}, err
	}
	log.Printf("[agent] LLM timeout, retrying after 5s: %v", err)
	time.Sleep(5 * time.Second)
	return client.CreateChatCompletion(ctx, req)
}

// convertToMessagesAPIFormat removes system-role messages and prepends their content to the first
// user message. The x402 router with model "auto" may route to Anthropic's Messages API, which
// rejects "system" as a message role and expects system as a top-level parameter. Prepending
// to the first user message is a compatible workaround.
func convertToMessagesAPIFormat(messages []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	var systemParts []string
	var out []openai.ChatCompletionMessage
	for _, m := range messages {
		if m.Role == openai.ChatMessageRoleSystem {
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}
			continue
		}
		out = append(out, m)
	}
	if len(systemParts) == 0 || len(out) == 0 {
		return out
	}
	// Prepend system to first user message
	systemBlock := strings.Join(systemParts, "\n\n")
	for i := range out {
		if out[i].Role == openai.ChatMessageRoleUser {
			out[i].Content = systemBlock + "\n\n---\n\n" + out[i].Content
			break
		}
	}
	return out
}

// extractRememberContent returns content to save when user explicitly asks to remember something.
func extractRememberContent(text string) string {
	text = strings.TrimSpace(text)
	if len(text) < 10 {
		return ""
	}
	lower := strings.ToLower(text)
	if !strings.HasPrefix(lower, "remember") && !strings.HasPrefix(lower, "memorize") {
		return ""
	}
	// "remember my name is X" -> "User's name is X"
	if m := rememberName.FindStringSubmatch(text); len(m) > 1 {
		name := strings.TrimSpace(m[1])
		if name != "" {
			return "User's name is " + name
		}
	}
	// "remember that X" or "remember: X" or "remember X"
	if loc := rememberPrefix.FindStringIndex(text); loc != nil {
		content := strings.TrimSpace(text[loc[1]:])
		if len(content) >= 3 {
			return content
		}
	}
	return ""
}
