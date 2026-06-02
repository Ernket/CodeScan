package utils

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadAndUnzipSizeLimits(t *testing.T) {
	if MaxUploadFileSize != 200*1024*1024 {
		t.Fatalf("expected upload limit to be 200MB, got %d", MaxUploadFileSize)
	}
	if MaxUnzippedSize != 500*1024*1024 {
		t.Fatalf("expected unzip limit to be 500MB, got %d", MaxUnzippedSize)
	}
}

func TestUnzipWithLimitRejectsOversizedContent(t *testing.T) {
	root := t.TempDir()
	zipPath := filepath.Join(root, "source.zip")
	destPath := filepath.Join(root, "out")

	if err := writeZipFixture(zipPath, map[string]string{
		"data.txt": strings.Repeat("a", 2048),
	}); err != nil {
		t.Fatalf("writeZipFixture() error = %v", err)
	}

	err := unzipWithLimit(zipPath, destPath, 1024)
	if err == nil {
		t.Fatal("expected unzipWithLimit to reject oversized extracted content")
	}
	if !strings.Contains(err.Error(), "unzipped size exceeds") {
		t.Fatalf("expected unzip error to mention size limit, got %v", err)
	}
}

func writeZipFixture(zipPath string, files map[string]string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer out.Close()

	writer := zip.NewWriter(out)

	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			return err
		}
	}

	return writer.Close()
}
