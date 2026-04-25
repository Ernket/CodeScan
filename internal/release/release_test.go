package release

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectReleaseFilesExcludesLocalArtifacts(t *testing.T) {
	root := t.TempDir()

	mustWriteFile(t, root, "main.go", "package main\n")
	mustWriteFile(t, root, "data/config.example.json", `{"api_key":"replace-with-api-key"}`)
	mustWriteFile(t, root, "data/config.json", `{"api_key":"replace-with-local-key"}`)
	mustWriteFile(t, root, "frontend/node_modules/app.js", "console.log('ignore')")
	mustWriteFile(t, root, "frontend/.cache/tmp.txt", "ignore")
	mustWriteFile(t, root, "release/out.zip", "ignore")
	mustWriteFile(t, root, "demo-report-20260425.html", "<html>ignore</html>")

	files, err := CollectReleaseFiles(root)
	if err != nil {
		t.Fatalf("CollectReleaseFiles() error = %v", err)
	}

	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.RelPath)
	}

	assertContains(t, got, "main.go")
	assertContains(t, got, "data/config.example.json")
	assertNotContains(t, got, "data/config.json")
	assertNotContains(t, got, "frontend/node_modules/app.js")
	assertNotContains(t, got, "frontend/.cache/tmp.txt")
	assertNotContains(t, got, "release/out.zip")
	assertNotContains(t, got, "demo-report-20260425.html")
}

func TestScanFilesFlagsSecrets(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "data/config.example.json", `{"api_key":"replace-with-api-key","password":""}`)

	unsafeKey := "sk-" + "abcdef1234567890abcdef1234567890"
	mustWriteFile(t, root, "unsafe.json", "{\n  \"auth_key\": \"abc123\",\n  \"password\": \"rootpw\",\n  \"api_key\": \""+unsafeKey+"\"\n}\n")

	files, err := CollectReleaseFiles(root)
	if err != nil {
		t.Fatalf("CollectReleaseFiles() error = %v", err)
	}

	findings, err := ScanFiles(files)
	if err != nil {
		t.Fatalf("ScanFiles() error = %v", err)
	}

	rules := make(map[string]bool, len(findings))
	for _, finding := range findings {
		rules[finding.Rule] = true
	}

	for _, rule := range []string{"config_auth_key", "config_password", "config_api_key", "openai_api_key"} {
		if !rules[rule] {
			t.Fatalf("expected finding for %s, got %+v", rule, findings)
		}
	}
}

func TestScanFilesIgnoresPlaceholderValues(t *testing.T) {
	root := t.TempDir()

	mustWriteFile(t, root, "README.md", "{\n  \"auth_key\": \"...\"\n}\n")
	mustWriteFile(t, root, "internal/config/config_test.go", "{\n  \"auth_key\": \"token\",\n  \"password\": \"secret\",\n  \"api_key\": \"k\"\n}\n")

	files, err := CollectReleaseFiles(root)
	if err != nil {
		t.Fatalf("CollectReleaseFiles() error = %v", err)
	}

	findings, err := ScanFiles(files)
	if err != nil {
		t.Fatalf("ScanFiles() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected placeholder values to be ignored, got %+v", findings)
	}
}

func TestCreateArchiveOmitsLocalConfig(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "release", "codescan-open-source.zip")

	mustWriteFile(t, root, "main.go", "package main\n")
	mustWriteFile(t, root, "README.md", "# CodeScan\n")
	mustWriteFile(t, root, "data/config.example.json", `{"api_key":"replace-with-api-key"}`)
	mustWriteFile(t, root, "data/config.json", `{"api_key":"replace-with-local-key"}`)

	files, err := CollectReleaseFiles(root)
	if err != nil {
		t.Fatalf("CollectReleaseFiles() error = %v", err)
	}
	if err := CreateArchive(files, out); err != nil {
		t.Fatalf("CreateArchive() error = %v", err)
	}

	reader, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("zip.OpenReader() error = %v", err)
	}
	defer reader.Close()

	paths := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		paths = append(paths, filepath.ToSlash(file.Name))
	}

	assertContains(t, paths, "data/config.example.json")
	assertNotContains(t, paths, "data/config.json")

	result, err := ValidateArchive(out)
	if err != nil {
		t.Fatalf("ValidateArchive() error = %v", err)
	}
	if len(result.UnexpectedEntries) != 0 {
		t.Fatalf("unexpected entries found: %v", result.UnexpectedEntries)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("unexpected secret findings: %+v", result.Findings)
	}
}

func mustWriteFile(t *testing.T, root, relPath, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", relPath, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", relPath, err)
	}
}

func assertContains(t *testing.T, values []string, target string) {
	t.Helper()

	for _, value := range values {
		if value == target {
			return
		}
	}
	t.Fatalf("expected %q in %v", target, values)
}

func assertNotContains(t *testing.T, values []string, target string) {
	t.Helper()

	for _, value := range values {
		if value == target {
			t.Fatalf("did not expect %q in %v", target, values)
		}
	}
}
