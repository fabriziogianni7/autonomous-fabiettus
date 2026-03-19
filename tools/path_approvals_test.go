package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathSafe_ValidRelativePath(t *testing.T) {
	tmp := t.TempDir()
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Create a file to resolve to
	if err := os.WriteFile(filepath.Join(tmp, "foo.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	resolved, err := resolvePathSafe("foo.txt")
	if err != nil {
		t.Fatalf("resolvePathSafe(foo.txt): %v", err)
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatalf("read resolved path: %v", err)
	}
	if string(content) != "x" {
		t.Errorf("read %q: got %q", resolved, content)
	}
}

func TestResolvePathSafe_RejectsPathTraversal(t *testing.T) {
	tmp := t.TempDir()
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	_, err := resolvePathSafe("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
	if err != nil && !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected 'escapes' in error, got: %v", err)
	}
}

func TestResolvePathSafe_RejectsAbsolutePathOutsideBase(t *testing.T) {
	tmp := t.TempDir()
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	_, err := resolvePathSafe("/etc/passwd")
	if err == nil {
		t.Error("expected error for absolute path outside base")
	}
}

func TestResolvePathSafe_RejectsEmptyPath(t *testing.T) {
	_, err := resolvePathSafe("")
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestIsSafe_HeadRequiresApproval(t *testing.T) {
	if IsSafe("head /etc/passwd") {
		t.Error("head /etc/passwd should not be safe (requires approval)")
	}
	if IsSafe("head -n 5 foo.txt") {
		t.Error("head -n 5 foo.txt should not be safe")
	}
}

func TestIsSafe_AllowedCommands(t *testing.T) {
	for _, cmd := range []string{"ls", "pwd", "whoami", "date", "id"} {
		if !IsSafe(cmd) {
			t.Errorf("%q should be safe", cmd)
		}
	}
}

func TestIsSafe_RemovedCommands(t *testing.T) {
	for _, cmd := range []string{"head", "tail", "wc", "file"} {
		if IsSafe(cmd) {
			t.Errorf("%q should not be safe (require approval)", cmd)
		}
	}
}
