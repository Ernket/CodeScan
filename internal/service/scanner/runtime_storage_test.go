package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicOverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	if err := writeFileAtomic(path, []byte("new")); err != nil {
		t.Fatalf("atomic overwrite: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overwritten file: %v", err)
	}
	if string(data) != "new" {
		t.Fatalf("expected overwritten content %q, got %q", "new", string(data))
	}
}
