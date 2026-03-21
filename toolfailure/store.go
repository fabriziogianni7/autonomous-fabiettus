// Package toolfailure provides append-only JSONL persistence for tool failure events.
package toolfailure

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	pathpkg "path/filepath"
	"sync"
	"time"

	"custom-agent/wallet/redact"
)

const (
	defaultDir   = "tool-failures"
	failuresFile = "failures.jsonl"

	// KindExecError is a Go error from tool execution (e.g. JSON parse, unknown tool).
	KindExecError = "exec_error"
	// KindErrorResult is an in-band "Error: ..." string returned as success.
	KindErrorResult = "error_result"
	// KindSubagentDenied is a forbidden tool for sub-agents.
	KindSubagentDenied = "subagent_denied"
	// KindSpawnSubagentsError is a spawn_subagents parse/runtime failure.
	KindSpawnSubagentsError = "spawn_subagents_error"
	// KindAnalyzerHint records a failure-analyzer output (optional).
	KindAnalyzerHint = "analyzer_hint"
	// KindCommonIssueAppend records a high-confidence COMMON_ISSUES.md append.
	KindCommonIssueAppend = "common_issue_append"
	// KindRecovery marks success after a retry (links via correlation_id).
	KindRecovery = "recovery"
)

// Entry is one append-only JSONL record (redacted fields).
type Entry struct {
	Timestamp       string `json:"ts"`
	CorrelationID   string `json:"correlation_id,omitempty"`
	Tool            string `json:"tool,omitempty"`
	ArgsSummary     string `json:"args_summary,omitempty"`
	Kind            string `json:"kind"`
	Message         string `json:"message,omitempty"`
	Platform        string `json:"platform,omitempty"`
	UserID          string `json:"user_id,omitempty"`
	ChatID          string `json:"chat_id,omitempty"`
	Source          string `json:"source,omitempty"` // main_agent, subagent, spawn_subagents
	Attempt         int    `json:"attempt,omitempty"`
	Resolved        bool   `json:"resolved,omitempty"`
	ResolutionNote  string `json:"resolution_note,omitempty"`
	SubagentRole    string `json:"subagent_role,omitempty"`
	SubagentTaskIdx int    `json:"subagent_task_idx,omitempty"`
}

// Store appends tool failure entries under a directory (typically DataRoot).
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore creates a store that writes to dir (e.g. filepath.Join(dataRoot, "tool-failures")).
func NewStore(dir string) *Store {
	if dir == "" {
		dir = defaultDir
	}
	return &Store{dir: dir}
}

func (s *Store) path() string {
	return pathpkg.Join(s.dir, failuresFile)
}

// Record appends one entry with redacted message and args summary. Safe if s is nil.
func (s *Store) Record(e Entry) {
	if s == nil {
		return
	}
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	e.Message = redact.Redact(truncate(e.Message, 2000))
	e.ArgsSummary = redact.Redact(truncate(e.ArgsSummary, 800))

	s.mu.Lock()
	defer s.mu.Unlock()

	// #nosec G301 -- 0750 restricts to owner+group
	if err := os.MkdirAll(s.dir, 0750); err != nil {
		return
	}
	f, err := os.OpenFile(s.path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(e)
}

// ReadRecentTail returns up to maxLines most recent entries (best-effort, scans whole file).
func (s *Store) ReadRecentTail(maxLines int) []Entry {
	if s == nil || maxLines <= 0 {
		return nil
	}
	s.mu.Lock()
	path := s.path()
	s.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	const maxScan = 10 * 1024 * 1024
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, maxScan)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	start := 0
	if len(lines) > maxLines {
		start = len(lines) - maxLines
	}
	var out []Entry
	for _, ln := range lines[start:] {
		var e Entry
		if json.Unmarshal(ln, &e) == nil && e.Kind != "" {
			out = append(out, e)
		}
	}
	return out
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
