package release

import (
	"archive/zip"
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type FileEntry struct {
	AbsPath string
	RelPath string
}

type SecretFinding struct {
	Path    string
	Line    int
	Rule    string
	Excerpt string
}

type ArchiveValidationResult struct {
	FileCount         int
	UnexpectedEntries []string
	Findings          []SecretFinding
}

var (
	openAIKeyPattern     = regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`)
	authKeyFieldPattern  = regexp.MustCompile(`(?i)"auth_key"\s*:\s*"([^"]+)"`)
	apiKeyFieldPattern   = regexp.MustCompile(`(?i)"api_key"\s*:\s*"([^"]+)"`)
	passwordFieldPattern = regexp.MustCompile(`(?i)"password"\s*:\s*"([^"]+)"`)
	dsnPattern           = regexp.MustCompile(`(?i)(?:mysql|postgres(?:ql)?|mongodb(?:\+srv)?)://[^/\s"'` + "`" + `]+:[^@\s"'` + "`" + `]+@`)
	privateKeyPattern    = regexp.MustCompile(`BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY`)
)

var excludedPrefixes = []string{
	".git/",
	".cache/",
	"bin/",
	"projects/",
	"release/",
	"frontend/.cache/",
	"frontend/node_modules/",
	"frontend/dist/",
}

var excludedExactPaths = map[string]struct{}{
	"data/config.json": {},
	"data/tasks.json":  {},
}

var excludedSuffixes = []string{
	".zip",
	".exe",
}

var excludedGlobs = []string{
	"*-report-*.html",
}

func CollectReleaseFiles(root string) ([]FileEntry, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	files := make([]FileEntry, 0, 128)
	err = filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if path == rootAbs {
			return nil
		}

		relPath, err := filepath.Rel(rootAbs, path)
		if err != nil {
			return err
		}
		relPath = normalizeReleasePath(relPath)

		if IsExcludedPath(relPath, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		files = append(files, FileEntry{
			AbsPath: path,
			RelPath: relPath,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})
	return files, nil
}

func IsExcludedPath(relPath string, isDir bool) bool {
	relPath = normalizeReleasePath(relPath)
	if relPath == "." || relPath == "" {
		return false
	}

	for _, prefix := range excludedPrefixes {
		trimmed := strings.TrimSuffix(prefix, "/")
		if relPath == trimmed || strings.HasPrefix(relPath, prefix) {
			return true
		}
	}

	if _, ok := excludedExactPaths[relPath]; ok {
		return true
	}

	if isDir {
		return false
	}

	lower := strings.ToLower(relPath)
	for _, suffix := range excludedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	for _, pattern := range excludedGlobs {
		matched, err := filepath.Match(pattern, filepath.Base(lower))
		if err == nil && matched {
			return true
		}
	}

	return false
}

func ScanFiles(files []FileEntry) ([]SecretFinding, error) {
	findings := make([]SecretFinding, 0)
	for _, file := range files {
		content, err := os.ReadFile(file.AbsPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file.RelPath, err)
		}
		findings = append(findings, scanContent(file.RelPath, content)...)
	}

	sortFindings(findings)
	return findings, nil
}

func CreateArchive(files []FileEntry, outPath string) error {
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outAbs), 0o755); err != nil {
		return err
	}

	outFile, err := os.Create(outAbs)
	if err != nil {
		return err
	}
	defer outFile.Close()

	zipWriter := zip.NewWriter(outFile)

	for _, file := range files {
		info, err := os.Stat(file.AbsPath)
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = file.RelPath
		header.Method = zip.Deflate

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}

		src, err := os.Open(file.AbsPath)
		if err != nil {
			return err
		}

		_, copyErr := io.Copy(writer, src)
		closeErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}

	return zipWriter.Close()
}

func ValidateArchive(zipPath string) (ArchiveValidationResult, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return ArchiveValidationResult{}, err
	}
	defer reader.Close()

	result := ArchiveValidationResult{}
	for _, file := range reader.File {
		relPath := normalizeReleasePath(file.Name)
		if file.FileInfo().IsDir() {
			if IsExcludedPath(relPath, true) {
				result.UnexpectedEntries = append(result.UnexpectedEntries, relPath)
			}
			continue
		}

		result.FileCount++
		if IsExcludedPath(relPath, false) {
			result.UnexpectedEntries = append(result.UnexpectedEntries, relPath)
			continue
		}

		rc, err := file.Open()
		if err != nil {
			return ArchiveValidationResult{}, err
		}
		content, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if err != nil {
			return ArchiveValidationResult{}, err
		}
		if closeErr != nil {
			return ArchiveValidationResult{}, closeErr
		}

		result.Findings = append(result.Findings, scanContent(relPath, content)...)
	}

	sort.Strings(result.UnexpectedEntries)
	sortFindings(result.Findings)
	return result, nil
}

func normalizeReleasePath(path string) string {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	return strings.TrimPrefix(cleaned, "./")
}

func scanContent(relPath string, content []byte) []SecretFinding {
	if len(content) == 0 || bytes.IndexByte(content, 0) >= 0 {
		return nil
	}

	findings := make([]SecretFinding, 0)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		findings = append(findings, scanLine(relPath, lineNo, line)...)
	}

	return findings
}

func scanLine(relPath string, lineNo int, line string) []SecretFinding {
	findings := make([]SecretFinding, 0, 4)
	appendFinding := func(rule string) {
		findings = append(findings, SecretFinding{
			Path:    relPath,
			Line:    lineNo,
			Rule:    rule,
			Excerpt: strings.TrimSpace(line),
		})
	}

	if openAIKeyPattern.MatchString(line) {
		appendFinding("openai_api_key")
	}

	if match := authKeyFieldPattern.FindStringSubmatch(line); len(match) == 2 && !isPlaceholderValue(match[1]) {
		appendFinding("config_auth_key")
	}

	if match := apiKeyFieldPattern.FindStringSubmatch(line); len(match) == 2 && !isPlaceholderValue(match[1]) {
		appendFinding("config_api_key")
	}

	if match := passwordFieldPattern.FindStringSubmatch(line); len(match) == 2 && !isPlaceholderValue(match[1]) {
		appendFinding("config_password")
	}

	if dsnPattern.MatchString(line) {
		appendFinding("credential_dsn")
	}

	if privateKeyPattern.MatchString(line) {
		appendFinding("private_key")
	}

	return findings
}

func isPlaceholderValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}

	lower := strings.ToLower(trimmed)
	switch lower {
	case "...", "***", "token", "secret", "dummy", "example", "sample", "test", "changeme", "k":
		return true
	}
	if strings.HasPrefix(lower, "replace-with-") {
		return true
	}
	if strings.HasPrefix(lower, "your-") {
		return true
	}
	if strings.Contains(lower, "placeholder") {
		return true
	}
	if strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}") {
		return true
	}
	if strings.HasPrefix(trimmed, "$") {
		return true
	}
	if strings.HasPrefix(trimmed, "%") && strings.HasSuffix(trimmed, "%") {
		return true
	}

	return false
}

func sortFindings(findings []SecretFinding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path == findings[j].Path {
			if findings[i].Line == findings[j].Line {
				return findings[i].Rule < findings[j].Rule
			}
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Path < findings[j].Path
	})
}
