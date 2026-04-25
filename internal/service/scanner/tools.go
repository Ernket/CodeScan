package scanner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
)

// Tool Definitions
var Tools = []openai.Tool{
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "read_file",
			Description: "Read the content of a file from the local file system. Supports reading specific lines.",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"path": {
						Type:        jsonschema.String,
						Description: "The absolute or relative path to the file to read",
					},
					"start_line": {
						Type:        jsonschema.Integer,
						Description: "The line number to start reading from (1-based, optional)",
					},
					"end_line": {
						Type:        jsonschema.Integer,
						Description: "The line number to end reading at (1-based, optional)",
					},
					"max_output_bytes": {
						Type:        jsonschema.Integer,
						Description: "Maximum output bytes before truncation (optional)",
					},
				},
				Required: []string{"path"},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "get_evidence",
			Description: "Retrieve a previously preserved read_file snippet by evidence ID after context compression.",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"evidence_id": {
						Type:        jsonschema.String,
						Description: "The evidence ID from the preserved read_file evidence index.",
					},
				},
				Required: []string{"evidence_id"},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "get_artifact",
			Description: "Retrieve a previously preserved artifact by ID after context compression. Prefer this for any indexed artifact, including read_file snippets and older tool outputs.",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"artifact_id": {
						Type:        jsonschema.String,
						Description: "The artifact ID from the preserved artifact index.",
					},
				},
				Required: []string{"artifact_id"},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "query_manifest",
			Description: "Query the lightweight project manifest for module roots, route candidate files, and stage hotspots.",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"stage": {
						Type:        jsonschema.String,
						Description: "Optional stage filter such as init, rce, auth, or logic.",
					},
					"module": {
						Type:        jsonschema.String,
						Description: "Optional module root filter from the manifest.",
					},
					"rule_name": {
						Type:        jsonschema.String,
						Description: "Optional rule name filter.",
					},
					"offset": {
						Type:        jsonschema.Integer,
						Description: "Skip the first N manifest hits (optional).",
					},
					"limit": {
						Type:        jsonschema.Integer,
						Description: "Maximum number of hits to return (optional).",
					},
				},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "query_routes",
			Description: "Query the structured route inventory without injecting the full route JSON into the prompt.",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"source": {
						Type:        jsonschema.String,
						Description: "Optional source file substring filter.",
					},
					"path": {
						Type:        jsonschema.String,
						Description: "Optional route path substring filter.",
					},
					"method": {
						Type:        jsonschema.String,
						Description: "Optional HTTP method filter.",
					},
					"offset": {
						Type:        jsonschema.Integer,
						Description: "Skip the first N routes (optional).",
					},
					"limit": {
						Type:        jsonschema.Integer,
						Description: "Maximum number of routes to return (optional).",
					},
				},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "query_stage_output",
			Description: "Query structured findings from the current stage or another completed stage without injecting the full findings JSON into the prompt.",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"stage": {
						Type:        jsonschema.String,
						Description: "Optional stage name. Defaults to the current stage when omitted.",
					},
					"origin": {
						Type:        jsonschema.String,
						Description: "Optional origin filter such as initial or gap_check.",
					},
					"verification_status": {
						Type:        jsonschema.String,
						Description: "Optional verification status filter such as confirmed, uncertain, rejected, or unreviewed.",
					},
					"offset": {
						Type:        jsonschema.Integer,
						Description: "Skip the first N findings (optional).",
					},
					"limit": {
						Type:        jsonschema.Integer,
						Description: "Maximum number of findings to return (optional).",
					},
				},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "list_files",
			Description: "List files and directories in a given path (non-recursive)",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"path": {
						Type:        jsonschema.String,
						Description: "The directory path to list. Defaults to current directory if empty.",
					},
					"max_entries": {
						Type:        jsonschema.Integer,
						Description: "Maximum number of entries to return (optional).",
					},
				},
				Required: []string{"path"},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "list_dir_tree",
			Description: "List directory structure recursively (tree view)",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"path": {
						Type:        jsonschema.String,
						Description: "The root directory path. Defaults to current directory if empty.",
					},
					"max_depth": {
						Type:        jsonschema.Integer,
						Description: "Maximum depth to traverse. Default is 2.",
					},
					"max_entries": {
						Type:        jsonschema.Integer,
						Description: "Maximum number of entries to return (optional).",
					},
				},
				Required: []string{"path"},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "search_files",
			Description: "Search for files by name pattern (Glob)",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"path": {
						Type:        jsonschema.String,
						Description: "The directory to search in. Defaults to current directory.",
					},
					"pattern": {
						Type:        jsonschema.String,
						Description: "The glob pattern (e.g., '*.go', 'config/*.json').",
					},
					"max_results": {
						Type:        jsonschema.Integer,
						Description: "Maximum number of results to return (optional).",
					},
					"offset": {
						Type:        jsonschema.Integer,
						Description: "Skip the first N matches (optional).",
					},
				},
				Required: []string{"pattern"},
			},
		},
	},
	{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "grep_files",
			Description: "Search for text content within files using Regex",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"path": {
						Type:        jsonschema.String,
						Description: "The directory to search in. Defaults to current directory.",
					},
					"pattern": {
						Type:        jsonschema.String,
						Description: "The regex pattern to search for.",
					},
					"case_insensitive": {
						Type:        jsonschema.Boolean,
						Description: "Whether search should be case insensitive.",
					},
					"max_results": {
						Type:        jsonschema.Integer,
						Description: "Maximum number of matched lines to return (optional).",
					},
					"offset": {
						Type:        jsonschema.Integer,
						Description: "Skip the first N matches (optional).",
					},
					"max_files": {
						Type:        jsonschema.Integer,
						Description: "Maximum number of files to scan (optional).",
					},
					"max_output_bytes": {
						Type:        jsonschema.Integer,
						Description: "Maximum output bytes before truncation (optional).",
					},
				},
				Required: []string{"pattern"},
			},
		},
	},
}

const (
	defaultReadMaxOutputBytes   = 500 * 1024
	defaultGrepMaxOutputBytes   = 300 * 1024
	defaultListMaxOutputBytes   = 200 * 1024
	defaultSearchMaxOutputBytes = 200 * 1024
	defaultReadMaxLines         = 200
	defaultGrepMaxResults       = 200
	defaultSearchMaxResults     = 200
	defaultListMaxEntries       = 1000
	defaultTreeMaxEntries       = 1500
	maxGrepFileBytes            = 2 * 1024 * 1024
)

var stopWalk = errors.New("stop")

type rgCommandResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
	err      error
}

var runRGCommand = defaultRunRGCommand

var skipDirNames = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"target":       {},
	"bin":          {},
	"obj":          {},
	".idea":        {},
	".vscode":      {},
	".next":        {},
	".nuxt":        {},
	".cache":       {},
	"coverage":     {},
	"project":      {},
	"projects":     {},
}

func GetStringArg(args map[string]interface{}, key string) string {
	if v, ok := args[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func GetFloatArg(args map[string]interface{}, key string) float64 {
	if v, ok := args[key]; ok && v != nil {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}

func shouldSkipDir(info os.FileInfo) bool {
	if !info.IsDir() {
		return false
	}
	_, ok := skipDirNames[info.Name()]
	return ok
}

func pathHasSkippedComponent(path string) bool {
	cleanPath := filepath.Clean(path)
	for _, part := range strings.Split(cleanPath, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		if _, ok := skipDirNames[part]; ok {
			return true
		}
	}
	return false
}

func ExecuteReadFile(path string, startLine, endLine int, maxOutputBytes int) string {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err)
	}
	defer f.Close()

	// Check file size
	_, err = f.Stat()
	if err != nil {
		return fmt.Sprintf("Error stating file: %v", err)
	}

	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultReadMaxOutputBytes
	}

	autoRange := false
	if startLine <= 0 && endLine <= 0 {
		startLine = 1
		endLine = defaultReadMaxLines
		autoRange = true
	}

	// 2. Normalize arguments
	if startLine < 1 {
		startLine = 1
	}
	if endLine > 0 && startLine > endLine {
		return fmt.Sprintf("Error: start_line (%d) > end_line (%d)", startLine, endLine)
	}

	// 3. Scan lines
	scanner := bufio.NewScanner(f)
	// Buffer up to 1MB per line to handle minified code
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var sb strings.Builder
	currentLine := 0

	// Safety: if endLine is not set, limit to startLine + 1000
	if endLine <= 0 {
		endLine = startLine + 1000
	}

	for scanner.Scan() {
		currentLine++
		if currentLine >= startLine {
			sb.WriteString(strconv.Itoa(currentLine))
			sb.WriteString(": ")
			sb.WriteString(scanner.Text())
			sb.WriteString("\n")
		}
		if currentLine >= endLine {
			break
		}
		if sb.Len() > maxOutputBytes {
			sb.WriteString("\n... (Output truncated) ...")
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Sprintf("Error scanning file: %v", err)
	}

	if sb.Len() == 0 && currentLine < startLine {
		return fmt.Sprintf("File is shorter than start_line (%d). Total lines: %d", startLine, currentLine)
	}

	if autoRange {
		sb.WriteString(fmt.Sprintf("\n... (Partial output: lines %d-%d. Use start_line/end_line for more.)", startLine, endLine))
	}

	return sb.String()
}

func ExecuteListFiles(path string, maxEntries int) string {
	files, err := os.ReadDir(path)
	if err != nil {
		return fmt.Sprintf("Error listing files: %v", err)
	}

	if maxEntries <= 0 {
		maxEntries = defaultListMaxEntries
	}

	lines := make([]string, 0, len(files))
	totalEntries := 0
	truncated := false
	currentBytes := 0
	for _, f := range files {
		info, _ := f.Info()
		if shouldSkipDir(info) {
			continue
		}
		totalEntries++
		prefix := "F"
		if f.IsDir() {
			prefix = "D"
		}
		line := fmt.Sprintf("[%s] %s (%d bytes)", prefix, f.Name(), info.Size())
		if len(lines) >= maxEntries || currentBytes+len(line)+1 > defaultListMaxOutputBytes {
			truncated = true
			continue
		}
		lines = append(lines, line)
		currentBytes += len(line) + 1
	}
	if totalEntries == 0 {
		return "Directory is empty."
	}
	body := strings.Join(lines, "\n")
	if truncated {
		if body != "" {
			body += "\n"
		}
		body += fmt.Sprintf("TRUNCATED: true\nRETURNED_ENTRIES: %d\nTOTAL_ENTRIES: %d", len(lines), totalEntries)
	}
	return body
}

func ExecuteListDirTree(path string, maxDepth int, maxEntries int) string {
	var sb strings.Builder
	rootDepth := strings.Count(filepath.Clean(path), string(os.PathSeparator))
	count := 0
	truncated := false

	if maxEntries <= 0 {
		maxEntries = defaultTreeMaxEntries
	}

	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipDir(info) {
			return filepath.SkipDir
		}

		currentDepth := strings.Count(p, string(os.PathSeparator)) - rootDepth
		if currentDepth < 0 {
			currentDepth = 0
		}
		if currentDepth > maxDepth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		indent := strings.Repeat("  ", currentDepth)
		prefix := "F"
		if info.IsDir() {
			prefix = "D"
		}
		line := fmt.Sprintf("%s[%s] %s\n", indent, prefix, info.Name())
		if count >= maxEntries || sb.Len()+len(line) > defaultListMaxOutputBytes {
			truncated = true
			return stopWalk
		}
		sb.WriteString(line)
		count++
		return nil
	})

	if err != nil && !errors.Is(err, stopWalk) {
		return fmt.Sprintf("Error walking directory: %v", err)
	}
	body := strings.TrimRight(sb.String(), "\n")
	if body == "" {
		body = "."
	}
	if truncated {
		body += fmt.Sprintf("\nTRUNCATED: true\nRETURNED_ENTRIES: %d\nMAX_ENTRIES: %d", count, maxEntries)
	}
	return body
}

func ExecuteSearchFiles(path string, pattern string, maxResults int, offset int) string {
	if maxResults <= 0 {
		maxResults = defaultSearchMaxResults
	}
	if offset < 0 {
		offset = 0
	}
	if os.PathSeparator != '/' {
		pattern = strings.ReplaceAll(pattern, "/", string(os.PathSeparator))
	}
	prefix := "**" + string(os.PathSeparator)
	if strings.HasPrefix(pattern, prefix) {
		pattern = strings.TrimPrefix(pattern, prefix)
	}
	hasDir := strings.Contains(pattern, string(os.PathSeparator))
	var sb strings.Builder
	scannedFiles := 0
	matchedResults := 0
	returnedResults := 0
	truncated := false

	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipDir(info) {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			scannedFiles++
		}

		var nameToMatch string
		if hasDir {
			relPath, err := filepath.Rel(path, p)
			if err != nil {
				return nil
			}
			nameToMatch = relPath
		} else {
			nameToMatch = info.Name()
		}

		match, err := filepath.Match(pattern, nameToMatch)
		if err != nil {
			return err
		}

		if match {
			relPath, _ := filepath.Rel(path, p)
			matchedResults++
			if matchedResults <= offset {
				return nil
			}
			if returnedResults >= maxResults {
				truncated = true
				return nil
			}
			line := filepath.ToSlash(relPath)
			if sb.Len()+len(line)+1 > defaultSearchMaxOutputBytes {
				truncated = true
				return nil
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			returnedResults++
		}
		return nil
	})

	if err != nil {
		return fmt.Sprintf("Error searching files: %v", err)
	}
	body := strings.TrimRight(sb.String(), "\n")
	if body == "" {
		body = "No matching files found."
	}
	nextOffset := -1
	if truncated {
		nextOffset = offset + returnedResults
	}
	return appendPaginationFooter(body, truncated, nextOffset, scannedFiles, matchedResults)
}

func ExecuteGrepFiles(path string, pattern string, caseInsensitive bool, maxResults int, offset int, maxFiles int, maxOutputBytes int) string {
	if maxResults <= 0 {
		maxResults = defaultGrepMaxResults
	}
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultGrepMaxOutputBytes
	}
	if offset < 0 {
		offset = 0
	}

	files, scannedFiles, filesTruncated, err := collectGrepCandidates(path, maxFiles)
	if err != nil {
		return fmt.Sprintf("Error walking directory: %v", err)
	}

	if result, ok := executeGrepFilesWithRG(path, files, scannedFiles, filesTruncated, pattern, caseInsensitive, maxResults, offset, maxOutputBytes); ok {
		return result
	}
	return executeGrepFilesFallback(files, scannedFiles, filesTruncated, pattern, caseInsensitive, maxResults, offset, maxOutputBytes)
}

type grepCandidate struct {
	absPath string
	relPath string
}

func collectGrepCandidates(root string, maxFiles int) ([]grepCandidate, int, bool, error) {
	candidates := []grepCandidate{}
	scannedFiles := 0
	truncated := false

	err := filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if shouldSkipDir(info) {
				return filepath.SkipDir
			}
			return nil
		}
		if maxFiles > 0 && scannedFiles >= maxFiles {
			truncated = true
			return stopWalk
		}
		scannedFiles++

		if info.Size() > maxGrepFileBytes {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".exe" || ext == ".dll" || ext == ".bin" || ext == ".git" {
			return nil
		}
		if strings.Contains(p, ".git"+string(os.PathSeparator)) {
			return nil
		}

		relPath, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		candidates = append(candidates, grepCandidate{
			absPath: p,
			relPath: filepath.ToSlash(relPath),
		})
		return nil
	})
	if err != nil && !errors.Is(err, stopWalk) {
		return nil, 0, false, err
	}
	return candidates, scannedFiles, truncated, nil
}

func executeGrepFilesWithRG(path string, files []grepCandidate, scannedFiles int, filesTruncated bool, pattern string, caseInsensitive bool, maxResults int, offset int, maxOutputBytes int) (string, bool) {
	type rgEvent struct {
		Type string `json:"type"`
		Data struct {
			Path struct {
				Text string `json:"text"`
			} `json:"path"`
			Lines struct {
				Text string `json:"text"`
			} `json:"lines"`
			LineNumber int `json:"line_number"`
		} `json:"data"`
	}

	var sb strings.Builder
	matchedCount := 0
	writtenCount := 0
	truncated := filesTruncated
	if len(files) == 0 {
		return formatGrepResult("", truncated, offset, writtenCount, scannedFiles, matchedCount), true
	}
	batches := chunkGrepCandidates(files)

	for _, batch := range batches {
		args := []string{"--json", "--line-number", "--color", "never", "--no-ignore", "--hidden"}
		if caseInsensitive {
			args = append(args, "-i")
		}
		args = append(args, "--", pattern)
		for _, file := range batch {
			args = append(args, file.relPath)
		}

		result := runRGCommand(path, args)
		switch {
		case result.err == nil:
		case result.exitCode == 1:
			continue
		default:
			return "", false
		}

		scanner := bufio.NewScanner(bytes.NewReader(result.stdout))
		lineBuf := make([]byte, 0, 64*1024)
		scanner.Buffer(lineBuf, 1024*1024)
		for scanner.Scan() {
			var event rgEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				return "", false
			}
			if event.Type != "match" {
				continue
			}
			matchedCount++
			if matchedCount <= offset {
				continue
			}
			if writtenCount >= maxResults {
				truncated = true
				continue
			}
			displayLine := strings.TrimSpace(event.Data.Lines.Text)
			if len(displayLine) > 100 {
				displayLine = displayLine[:100] + "..."
			}
			resultLine := fmt.Sprintf("%s:%d: %s\n", filepath.ToSlash(event.Data.Path.Text), event.Data.LineNumber, displayLine)
			if sb.Len()+len(resultLine) > maxOutputBytes {
				truncated = true
				continue
			}
			sb.WriteString(resultLine)
			writtenCount++
		}
		if err := scanner.Err(); err != nil {
			return "", false
		}
	}

	return formatGrepResult(sb.String(), truncated, offset, writtenCount, scannedFiles, matchedCount), true
}

func executeGrepFilesFallback(files []grepCandidate, scannedFiles int, filesTruncated bool, pattern string, caseInsensitive bool, maxResults int, offset int, maxOutputBytes int) string {
	var sb strings.Builder
	prefix := ""
	if caseInsensitive {
		prefix = "(?i)"
	}

	re, err := regexp.Compile(prefix + pattern)
	if err != nil {
		return fmt.Sprintf("Invalid regex pattern: %v", err)
	}

	matchedCount := 0
	writtenCount := 0
	truncated := filesTruncated
	for _, file := range files {
		content, err := os.ReadFile(file.absPath)
		if err != nil {
			continue
		}

		lineNum := 0
		lineScanner := bufio.NewScanner(strings.NewReader(string(content)))
		lineBuf := make([]byte, 0, 64*1024)
		lineScanner.Buffer(lineBuf, 1024*1024)
		for lineScanner.Scan() {
			lineNum++
			line := lineScanner.Text()
			if !re.MatchString(line) {
				continue
			}
			matchedCount++
			if matchedCount <= offset {
				continue
			}
			if writtenCount >= maxResults {
				truncated = true
				continue
			}
			displayLine := strings.TrimSpace(line)
			if len(displayLine) > 100 {
				displayLine = displayLine[:100] + "..."
			}
			resultLine := fmt.Sprintf("%s:%d: %s\n", file.relPath, lineNum, displayLine)
			if sb.Len()+len(resultLine) > maxOutputBytes {
				truncated = true
				continue
			}
			sb.WriteString(resultLine)
			writtenCount++
		}
	}

	return formatGrepResult(sb.String(), truncated, offset, writtenCount, scannedFiles, matchedCount)
}

func chunkGrepCandidates(files []grepCandidate) [][]grepCandidate {
	if len(files) == 0 {
		return nil
	}

	const (
		maxChunkFiles = 256
		maxChunkBytes = 24 * 1024
	)

	chunks := [][]grepCandidate{}
	current := make([]grepCandidate, 0, maxChunkFiles)
	currentBytes := 0
	for _, file := range files {
		fileBytes := len(file.relPath) + 1
		if len(current) > 0 && (len(current) >= maxChunkFiles || currentBytes+fileBytes > maxChunkBytes) {
			chunks = append(chunks, current)
			current = make([]grepCandidate, 0, maxChunkFiles)
			currentBytes = 0
		}
		current = append(current, file)
		currentBytes += fileBytes
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func formatGrepResult(body string, truncated bool, offset int, writtenCount int, scannedFiles int, matchedCount int) string {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		body = "No matches found."
	}
	nextOffset := -1
	if truncated {
		nextOffset = offset + writtenCount
	}
	return appendPaginationFooter(body, truncated, nextOffset, scannedFiles, matchedCount)
}

func defaultRunRGCommand(dir string, args []string) rgCommandResult {
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		return rgCommandResult{err: err}
	}

	cmd := exec.Command(rgPath, args...)
	cmd.Dir = dir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return rgCommandResult{
		stdout:   stdout.Bytes(),
		stderr:   stderr.Bytes(),
		exitCode: exitCode,
		err:      err,
	}
}

func appendPaginationFooter(body string, truncated bool, nextOffset, scannedFiles, matchedResults int) string {
	if body == "" {
		body = "No matches found."
	}
	return fmt.Sprintf(
		"%s\nTRUNCATED: %t\nNEXT_OFFSET: %d\nSCANNED_FILES: %d\nMATCHED_RESULTS: %d",
		body,
		truncated,
		nextOffset,
		scannedFiles,
		matchedResults,
	)
}
