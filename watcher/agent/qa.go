package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"custom-agent/watcher/activity"

	"github.com/sashabaranov/go-openai"
)

const defaultModel = "llama-3.1-8b-instant"
const activityLimit = 25

// Agent is a minimal Q&A agent for answering questions about Fabietto.
type Agent struct {
	client       *openai.Client
	model        string
	systemPrompt string
	store        *activity.Store
}

// New creates a Q&A agent.
func New(client *openai.Client, systemPrompt string, store *activity.Store) *Agent {
	return &Agent{
		client:       client,
		model:        defaultModel,
		systemPrompt: systemPrompt,
		store:        store,
	}
}

// Respond processes a user message and returns the assistant reply.
func (a *Agent) Respond(ctx context.Context, userText string) string {
	entries, err := a.store.List(activityLimit)
	if err != nil {
		log.Printf("[watcher] activity list: %v", err)
	}
	activityBlock := formatActivity(entries)
	system := a.systemPrompt
	if activityBlock != "" {
		system += "\n\nRecent onchain activity:\n" + activityBlock
	}
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: system},
		{Role: openai.ChatMessageRoleUser, Content: strings.TrimSpace(userText)},
	}
	resp, err := a.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    a.model,
		Messages: messages,
	})
	if err != nil {
		log.Printf("[watcher] chat completion: %v", err)
		return "Sorry, I had trouble answering. Try again."
	}
	if len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content)
}

func formatActivity(entries []activity.Entry) string {
	if len(entries) == 0 {
		return "(no recent activity)"
	}
	var lines []string
	for _, e := range entries {
		asset := e.Asset
		if asset == "" {
			asset = "token"
		}
		valStr := fmt.Sprintf("%.4f", e.Value)
		if e.Value >= 1000 {
			valStr = fmt.Sprintf("%.0f", e.Value)
		} else if e.Value >= 1 {
			valStr = fmt.Sprintf("%.2f", e.Value)
		}
		lines = append(lines, fmt.Sprintf("- %s: %s %s on %s",
			e.Timestamp.Format("2006-01-02 15:04"), valStr, asset, e.Network))
	}
	return strings.Join(lines, "\n")
}
