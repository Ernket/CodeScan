//go:build windows

package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFileAtomicRetriesTransientWindowsReadSharing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	handle, err := os.Open(path)
	if err != nil {
		t.Fatalf("open existing file: %v", err)
	}

	closeDone := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		closeDone <- handle.Close()
	}()

	if err := writeFileAtomic(path, []byte("new")); err != nil {
		_ = handle.Close()
		t.Fatalf("atomic overwrite while a transient read handle is open: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close read handle: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overwritten file: %v", err)
	}
	if string(data) != "new" {
		t.Fatalf("expected overwritten content %q, got %q", "new", string(data))
	}
}
