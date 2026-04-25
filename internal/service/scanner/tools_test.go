package scanner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteListFilesSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "project"))
	mustMkdirAll(t, filepath.Join(root, "projects"))
	mustMkdirAll(t, filepath.Join(root, "src"))

	result := ExecuteListFiles(root, 100)

	if strings.Contains(result, "[D] project") || strings.Contains(result, "[D] projects") {
		t.Fatalf("expected ignored directories to be hidden, got %q", result)
	}
	if !strings.Contains(result, "[D] src") {
		t.Fatalf("expected non-ignored directory to remain visible, got %q", result)
	}
}

func TestRecursiveToolsSkipIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "project", "ignored.go"), "package project\nconst marker = \"SKIP_ME\"\n")
	mustWriteFile(t, filepath.Join(root, "projects", "ignored.go"), "package projects\nconst marker = \"SKIP_ME_TOO\"\n")
	mustWriteFile(t, filepath.Join(root, "src", "keep.go"), "package src\nconst marker = \"KEEP_ME\"\n")

	treeResult := ExecuteListDirTree(root, 4, 100)
	if strings.Contains(treeResult, "project") || strings.Contains(treeResult, "projects") {
		t.Fatalf("expected tree listing to skip ignored directories, got %q", treeResult)
	}

	searchResult := ExecuteSearchFiles(root, "*.go", 100, 0)
	if strings.Contains(searchResult, filepath.ToSlash(filepath.Join("project", "ignored.go"))) || strings.Contains(searchResult, filepath.ToSlash(filepath.Join("projects", "ignored.go"))) {
		t.Fatalf("expected search to skip ignored directories, got %q", searchResult)
	}
	if !strings.Contains(searchResult, filepath.ToSlash(filepath.Join("src", "keep.go"))) {
		t.Fatalf("expected search to include non-ignored files, got %q", searchResult)
	}

	grepResult := ExecuteGrepFiles(root, "SKIP_ME|KEEP_ME", false, 100, 0, 100, 32*1024)
	if strings.Contains(grepResult, filepath.ToSlash(filepath.Join("project", "ignored.go"))) || strings.Contains(grepResult, filepath.ToSlash(filepath.Join("projects", "ignored.go"))) {
		t.Fatalf("expected grep to skip ignored directories, got %q", grepResult)
	}
	if !strings.Contains(grepResult, filepath.ToSlash(filepath.Join("src", "keep.go"))) {
		t.Fatalf("expected grep to include non-ignored files, got %q", grepResult)
	}
}

func TestExecuteReadFileAddsLineNumbers(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sample.go")
	mustWriteFile(t, file, "first line\nsecond line\nthird line\n")

	result := ExecuteReadFile(file, 2, 3, 0)

	if !strings.Contains(result, "2: second line\n3: third line\n") {
		t.Fatalf("expected stable line-numbered output, got %q", result)
	}
}

func TestExecuteReadFileAutoRangeKeepsFooter(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sample.go")
	mustWriteFile(t, file, "first line\nsecond line\n")

	result := ExecuteReadFile(file, 0, 0, 0)

	if !strings.Contains(result, "1: first line\n2: second line\n") {
		t.Fatalf("expected auto-range output to include line numbers, got %q", result)
	}
	if !strings.Contains(result, "... (Partial output: lines 1-200. Use start_line/end_line for more.)") {
		t.Fatalf("expected auto-range footer to remain, got %q", result)
	}
}

func TestExecuteReadFileShorterThanStartLineIsStillFailure(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sample.go")
	mustWriteFile(t, file, "only one line\n")

	result := ExecuteReadFile(file, 10, 12, 0)

	if isSuccessfulReadResult(result) {
		t.Fatalf("expected short-file branch to stay unsuccessful, got %q", result)
	}
}

func TestExecuteSearchFilesAppendsPaginationFooter(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "src", "keep.go"), "package src\n")

	result := ExecuteSearchFiles(root, "*.go", 10, 0)

	if !strings.Contains(result, "TRUNCATED: false") || !strings.Contains(result, "NEXT_OFFSET: -1") || !strings.Contains(result, "MATCHED_RESULTS: 1") {
		t.Fatalf("expected pagination footer, got %q", result)
	}
}

func TestExecuteGrepFilesWithoutMaxFilesScansAllFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 1105; i++ {
		content := "package sample\n"
		if i == 1104 {
			content += "const marker = \"TAIL_MATCH\"\n"
		}
		mustWriteFile(t, filepath.Join(root, "src", fmt.Sprintf("file-%04d.go", i)), content)
	}

	result := ExecuteGrepFiles(root, "TAIL_MATCH", false, 10, 0, 0, 64*1024)

	if !strings.Contains(result, "file-1104.go") {
		t.Fatalf("expected grep to scan beyond the old 1000-file limit, got %q", result)
	}
	if !strings.Contains(result, "SCANNED_FILES: 1105") {
		t.Fatalf("expected footer to record scanned files, got %q", result)
	}
}

func TestExecuteGrepFilesPrefersRGWhenAvailable(t *testing.T) {
	prevRunRGCommand := runRGCommand
	defer func() {
		runRGCommand = prevRunRGCommand
	}()

	callCount := 0
	runRGCommand = func(dir string, args []string) rgCommandResult {
		callCount++
		return rgCommandResult{
			stdout: []byte(`{"type":"match","data":{"path":{"text":"src/keep.go"},"lines":{"text":"const marker = \"KEEP_ME\"\n"},"line_number":2}}` + "\n"),
		}
	}

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "src", "keep.go"), "package src\nconst marker = \"KEEP_ME\"\n")

	result := ExecuteGrepFiles(root, "KEEP_ME", false, 10, 0, 0, 64*1024)

	if callCount == 0 {
		t.Fatal("expected ExecuteGrepFiles to invoke rg first")
	}
	if !strings.Contains(result, "src/keep.go:2: const marker = \"KEEP_ME\"") {
		t.Fatalf("expected rg result to be normalized into grep output, got %q", result)
	}
}

func TestExecuteGrepFilesFallsBackWhenRGUnavailable(t *testing.T) {
	prevRunRGCommand := runRGCommand
	defer func() {
		runRGCommand = prevRunRGCommand
	}()

	runRGCommand = func(dir string, args []string) rgCommandResult {
		return rgCommandResult{err: exec.ErrNotFound}
	}

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "src", "keep.go"), "package src\nconst marker = \"KEEP_ME\"\n")

	result := ExecuteGrepFiles(root, "KEEP_ME", false, 10, 0, 0, 64*1024)

	if !strings.Contains(result, "src/keep.go:2: const marker = \"KEEP_ME\"") {
		t.Fatalf("expected Go fallback to return grep results, got %q", result)
	}
}

func TestResolveToolPathRejectsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()

	_, err := resolveToolPath(root, filepath.Join("project", "ignored.go"))
	if err == nil || !strings.Contains(err.Error(), "ignored directory") {
		t.Fatalf("expected ignored directory error for project path, got %v", err)
	}

	_, err = resolveToolPath(root, filepath.Join("projects", "ignored.go"))
	if err == nil || !strings.Contains(err.Error(), "ignored directory") {
		t.Fatalf("expected ignored directory error for projects path, got %v", err)
	}

	resolved, err := resolveToolPath(root, filepath.Join("src", "keep.go"))
	if err != nil {
		t.Fatalf("expected non-ignored path to resolve, got %v", err)
	}
	if !strings.HasSuffix(resolved, filepath.Join("src", "keep.go")) {
		t.Fatalf("expected resolved path to point to src/keep.go, got %q", resolved)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
