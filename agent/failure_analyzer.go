package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	pathpkg "path/filepath"
	"strings"
	"time"

	"custom-agent/gateway"
	"custom-agent/toolfailure"
	"custom-agent/wallet/redact"
)

const (
	failureAnalyzerSystemPrompt = `You are a failure-analysis sub-agent. You only diagnose tool failures and suggest safe recovery steps.
Reply with a single JSON object only (no markdown fences, no prose outside JSON). Use this exact shape:
{"probable_cause":"short string","confidence":0.0,"safe_retry_recommendation":"short actionable step","append_common_issue":false,"common_issue_markdown":""}
Rules:
- Never include secrets, private keys, bearer tokens, or raw env values.
- append_common_issue must be false unless confidence is at least 0.85 and the fix is reusable across sessions.
- common_issue_markdown must be a single terse bullet line (tool name + symptom + fix) if append_common_issue is true.`

	failureAnalyzerTaskMax = 12000
	commonIssueMaxAppend   = 2000
)

func buildAnalyzerTask(tool, argsSummary, errText, recentFailures, commonIssues string) string {
	var b strings.Builder
	b.WriteString("Diagnose this tool failure and fill the JSON schema.\n\n")
	b.WriteString("Failed tool: ")
	b.WriteString(tool)
	b.WriteString("\nRedacted args (truncated): ")
	b.WriteString(truncateForLog(redact.Redact(argsSummary), 600))
	b.WriteString("\nRedacted error/result: ")
	b.WriteString(truncateForLog(redact.Redact(errText), 800))
	b.WriteString("\n\nRecent failure log lines (may be empty):\n")
	if strings.TrimSpace(recentFailures) == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(recentFailures)
	}
	b.WriteString("\n\nCOMMON_ISSUES excerpt (may be empty):\n")
	if strings.TrimSpace(commonIssues) == "" {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(commonIssues)
	}
	return b.String()
}

func (a *Agent) readCommonIssuesExcerpt(maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	paths := []string{}
	if a.dataRoot != "" {
		paths = append(paths, pathpkg.Join(a.dataRoot, "COMMON_ISSUES.md"))
	}
	paths = append(paths, "COMMON_ISSUES.md")
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil || len(b) == 0 {
			continue
		}
		s := strings.TrimSpace(string(b))
		if len(s) > maxLen {
			s = s[:maxLen] + "\n... truncated"
		}
		return s
	}
	return ""
}

type analyzerResponse struct {
	ProbableCause           string  `json:"probable_cause"`
	Confidence              float64 `json:"confidence"`
	SafeRetryRecommendation string  `json:"safe_retry_recommendation"`
	AppendCommonIssue       bool    `json:"append_common_issue"`
	CommonIssueMarkdown     string  `json:"common_issue_markdown"`
}

func parseAnalyzerJSON(text string) *analyzerResponse {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s) {
		s = s[:j+1]
	}
	var out analyzerResponse
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return &out
}

func (a *Agent) appendCommonIssueMarkdown(md string) error {
	if a.dataRoot == "" {
		return nil
	}
	md = strings.TrimSpace(redact.Redact(md))
	if md == "" {
		return nil
	}
	if len(md) > commonIssueMaxAppend {
		md = md[:commonIssueMaxAppend] + "..."
	}
	path := pathpkg.Join(a.dataRoot, "COMMON_ISSUES.md")
	// #nosec G301
	if err := os.MkdirAll(a.dataRoot, 0750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	line := "\n" + md
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	if a.failureStore != nil {
		a.failureStore.Record(toolfailure.Entry{
			Tool:    "COMMON_ISSUES.md",
			Kind:    toolfailure.KindCommonIssueAppend,
			Message: truncateForLog(md, 500),
			Source:  "failure_analyzer",
		})
	}
	return nil
}

// runFailureAnalyzer runs one bounded failure-analyzer subagent and returns a system hint (or empty).
func (a *Agent) runFailureAnalyzer(ctx context.Context, msg gateway.IncomingMessage, tool, argsJSON string, execErr error, result, correlationID string) string {
	if a.disableSubagents || !a.failureAnalyzerEnabled {
		return ""
	}
	recent := ""
	if a.failureStore != nil {
		entries := a.failureStore.ReadRecentTail(15)
		var b strings.Builder
		for _, e := range entries {
			b.WriteString(e.Tool)
			b.WriteString(" [")
			b.WriteString(e.Kind)
			b.WriteString("]: ")
			b.WriteString(truncateForLog(e.Message, 100))
			b.WriteString("\n")
		}
		recent = b.String()
	}
	ci := a.readCommonIssuesExcerpt(2500)
	task := buildAnalyzerTask(tool, argsJSON, failureMessage(execErr, result), recent, ci)
	if len(task) > failureAnalyzerTaskMax {
		task = task[:failureAnalyzerTaskMax] + "...[truncated]"
	}
	specs := []SubtaskSpec{{Task: task, Role: "failure-analyzer", Index: 0}}
	opts := &SubagentOpts{PerChildTimeout: 60 * time.Second}
	results, err := a.RunSubagents(ctx, specs, msg, opts)
	if err != nil || len(results) == 0 || results[0].Err != nil {
		return ""
	}
	out := parseAnalyzerJSON(results[0].Output)
	if out == nil {
		return ""
	}
	if out.AppendCommonIssue && out.Confidence >= 0.85 && strings.TrimSpace(out.CommonIssueMarkdown) != "" {
		if err := a.appendCommonIssueMarkdown(out.CommonIssueMarkdown); err != nil {
			log.Printf("[failure-analyzer] append COMMON_ISSUES: %v", err)
		}
	}
	if a.failureStore != nil {
		a.failureStore.Record(toolfailure.Entry{
			CorrelationID: correlationID,
			Tool:          tool,
			Kind:          toolfailure.KindAnalyzerHint,
			Message:       truncateForLog(out.ProbableCause+"; "+out.SafeRetryRecommendation, 1500),
			Platform:      msg.Platform,
			UserID:        msg.UserID,
			ChatID:        msg.ChatID,
			Source:        "failure_analyzer",
		})
	}
	var sb strings.Builder
	sb.WriteString("Tool failure diagnosis (internal): ")
	sb.WriteString(strings.TrimSpace(out.ProbableCause))
	sb.WriteString("\nSafe next step: ")
	sb.WriteString(strings.TrimSpace(out.SafeRetryRecommendation))
	sb.WriteString(fmt.Sprintf("\n(Analyzer confidence: %.2f)", out.Confidence))
	return sb.String()
}
