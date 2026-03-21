package toolfailure

import (
	"encoding/json"
	"os"
	pathpkg "path/filepath"
	"strings"
	"testing"
)

func TestStore_RecordAndReadTail(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.Record(Entry{
		Tool:    "http_request",
		Kind:    KindErrorResult,
		Message: "Error: timeout",
		Source:  "main_agent",
	})
	s.Record(Entry{
		Tool:    "web_search",
		Kind:    KindExecError,
		Message: "invalid arguments",
		Source:  "main_agent",
	})
	data, err := os.ReadFile(pathpkg.Join(dir, failuresFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 jsonl lines, got %d: %s", len(lines), data)
	}
	var e Entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Tool != "http_request" || e.Kind != KindErrorResult {
		t.Fatalf("first entry: %+v", e)
	}
	recent := s.ReadRecentTail(1)
	if len(recent) != 1 || recent[0].Tool != "web_search" {
		t.Fatalf("ReadRecentTail: %+v", recent)
	}
}
