package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerGet_RejectsInvalidName(t *testing.T) {
	tmp := t.TempDir()
	m := NewManager(tmp)
	// Create a valid skill so dir exists
	if err := os.MkdirAll(filepath.Join(tmp, "valid"), 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "valid", "SKILL.md"), []byte("# Valid\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := m.Get("../etc")
	if err != os.ErrInvalid {
		t.Errorf("Get(\"../etc\") should return ErrInvalid, got %v", err)
	}

	_, err = m.Get("foo/bar")
	if err != os.ErrInvalid {
		t.Errorf("Get(\"foo/bar\") should return ErrInvalid, got %v", err)
	}

	_, err = m.Get("valid")
	if err != nil {
		t.Errorf("Get(\"valid\") should succeed, got %v", err)
	}
}
