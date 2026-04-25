package scanner

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"codescan/internal/model"

	"github.com/sashabaranov/go-openai"
)

const (
	maxToolCacheEntries      = 200
	maxToolCacheBytes        = 8 * 1024 * 1024
	toolExecutionParallelism = 4
)

var concurrentReadOnlyTools = map[string]struct{}{
	"read_file":          {},
	"list_files":         {},
	"list_dir_tree":      {},
	"search_files":       {},
	"grep_files":         {},
	"query_manifest":     {},
	"query_routes":       {},
	"query_stage_output": {},
	"get_artifact":       {},
	"get_evidence":       {},
}

type cachedToolResult struct {
	Content     string
	ArtifactID  string
	OutputBytes int
}

func (r cachedToolResult) cacheBytes(key string) int {
	return len(key) + len(r.Content) + len(r.ArtifactID)
}

type toolResultCache struct {
	mu         sync.RWMutex
	entries    map[string]cachedToolResult
	order      []string
	totalBytes int
	maxEntries int
	maxBytes   int
}

func newToolResultCache(maxEntries, maxBytes int) *toolResultCache {
	if maxEntries <= 0 {
		maxEntries = maxToolCacheEntries
	}
	if maxBytes <= 0 {
		maxBytes = maxToolCacheBytes
	}
	return &toolResultCache{
		entries:    map[string]cachedToolResult{},
		order:      []string{},
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
	}
}

func (c *toolResultCache) load(key string) (cachedToolResult, bool) {
	if c == nil || key == "" {
		return cachedToolResult{}, false
	}

	c.mu.RLock()
	result, ok := c.entries[key]
	c.mu.RUnlock()
	return result, ok
}

func (c *toolResultCache) store(key string, result cachedToolResult) {
	if c == nil || key == "" {
		return
	}

	entryBytes := result.cacheBytes(key)
	if entryBytes > c.maxBytes {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.entries[key]; ok {
		c.totalBytes -= existing.cacheBytes(key)
		c.entries[key] = result
		c.totalBytes += entryBytes
	} else {
		c.entries[key] = result
		c.order = append(c.order, key)
		c.totalBytes += entryBytes
	}

	for len(c.order) > c.maxEntries || c.totalBytes > c.maxBytes {
		if len(c.order) == 0 {
			break
		}
		oldest := c.order[0]
		c.order = c.order[1:]
		if existing, ok := c.entries[oldest]; ok {
			c.totalBytes -= existing.cacheBytes(oldest)
			delete(c.entries, oldest)
		}
	}
}

type readFileArtifactPlan struct {
	resolvedPath string
	startLine    int
	endLine      int
}

type sharedToolExecution struct {
	toolName    string
	execute     func() string
	rawResult   string
	outputBytes int
	durationMs  int64

	finalized   bool
	finalResult cachedToolResult
}

func (e *sharedToolExecution) run() {
	start := time.Now()
	result := e.execute()
	e.rawResult = result
	e.outputBytes = len(result)
	e.durationMs = time.Since(start).Milliseconds()
}

type plannedToolCall struct {
	toolCall            openai.ToolCall
	toolName            string
	logArguments        string
	canonicalKey        string
	cacheable           bool
	concurrent          bool
	immediateResult     string
	cachedResult        cachedToolResult
	hasCachedResult     bool
	roundDuplicate      bool
	execution           *sharedToolExecution
	readFileArtifactRef *readFileArtifactPlan
}

type toolPlanningContext struct {
	task         *model.Task
	currentStage *model.TaskStage
	session      *scanSession
	toolCache    *toolResultCache
}

func executeToolRound(
	planCtx toolPlanningContext,
	toolCalls []openai.ToolCall,
	logFunc func(string),
) ([]plannedToolCall, error) {
	planned := make([]plannedToolCall, 0, len(toolCalls))
	sharedByKey := map[string]*sharedToolExecution{}
	parallelJobs := make([]*sharedToolExecution, 0, len(toolCalls))

	for _, toolCall := range toolCalls {
		callPlan := buildToolCallPlan(planCtx, toolCall)
		if callPlan.immediateResult != "" || callPlan.hasCachedResult {
			planned = append(planned, callPlan)
			continue
		}

		if callPlan.cacheable && callPlan.canonicalKey != "" {
			if shared, ok := sharedByKey[callPlan.canonicalKey]; ok {
				callPlan.execution = shared
				callPlan.roundDuplicate = true
				planned = append(planned, callPlan)
				continue
			}
		}

		execution := callPlan.execution
		if execution == nil {
			execution = &sharedToolExecution{
				toolName: callPlan.toolName,
				execute:  func() string { return fmt.Sprintf("Error: Unknown tool %s", callPlan.toolName) },
			}
		}
		callPlan.execution = execution
		if callPlan.cacheable && callPlan.canonicalKey != "" {
			sharedByKey[callPlan.canonicalKey] = execution
		}

		if callPlan.concurrent {
			parallelJobs = append(parallelJobs, execution)
		} else {
			execution.run()
		}
		planned = append(planned, callPlan)
	}

	runConcurrentToolExecutions(parallelJobs)

	for i := range planned {
		plan := &planned[i]
		var prepared cachedToolResult
		switch {
		case plan.hasCachedResult:
			prepared = plan.cachedResult
			logFunc(fmt.Sprintf("Tool cache hit: %s (%s)", plan.toolName, plan.logArguments))
		case plan.immediateResult != "":
			prepared = cachedToolResult{
				Content:     plan.immediateResult,
				OutputBytes: len(plan.immediateResult),
			}
		default:
			if plan.roundDuplicate {
				logFunc(fmt.Sprintf("Tool round dedupe hit: %s (%s)", plan.toolName, plan.logArguments))
			} else {
				logFunc(fmt.Sprintf("Executing tool: %s (%s)", plan.toolName, plan.logArguments))
			}

			var err error
			prepared, err = finalizeToolCallResult(planCtx, plan, logFunc)
			if err != nil {
				return nil, err
			}
		}

		logFunc(fmt.Sprintf("Tool %s finished in %d ms, output %d bytes", plan.toolName, toolPlanDurationMs(plan), prepared.OutputBytes))

		if err := planCtx.session.appendToolMessage(plan.toolCall.ID, plan.toolName, prepared.Content, prepared.ArtifactID); err != nil {
			return nil, err
		}
	}

	return planned, nil
}

func runConcurrentToolExecutions(jobs []*sharedToolExecution) {
	if len(jobs) == 0 {
		return
	}

	workers := toolExecutionParallelism
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers <= 1 {
		for _, job := range jobs {
			job.run()
		}
		return
	}

	jobCh := make(chan *sharedToolExecution)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				job.run()
			}
		}()
	}

	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)
	wg.Wait()
}

func finalizeToolCallResult(planCtx toolPlanningContext, plan *plannedToolCall, logFunc func(string)) (cachedToolResult, error) {
	if plan.execution == nil {
		message := fmt.Sprintf("Error: Unknown tool %s", plan.toolName)
		return cachedToolResult{Content: message, OutputBytes: len(message)}, nil
	}

	if plan.execution.finalized {
		return plan.execution.finalResult, nil
	}

	rawResult := plan.execution.rawResult
	artifactID := ""
	if plan.toolName == "read_file" && plan.readFileArtifactRef != nil && isSuccessfulReadResult(rawResult) {
		effectiveStartLine, effectiveEndLine := normalizeReadFileRange(plan.readFileArtifactRef.startLine, plan.readFileArtifactRef.endLine)
		record, recordErr := planCtx.session.createArtifact(
			"read_file",
			plan.toolName,
			displayToolPath(planCtx.task.BasePath, plan.readFileArtifactRef.resolvedPath),
			effectiveStartLine,
			effectiveEndLine,
			rawResult,
		)
		if recordErr == nil {
			artifactID = record.ID
			logFunc(fmt.Sprintf("Preserved read_file artifact %s for %s (%s).", record.ID, record.Path, formatLineRange(record.StartLine, record.EndLine)))
		}
	}

	content, artifactID, err := prepareToolMessageForTranscript(planCtx.session, plan.toolName, rawResult, artifactID)
	if err != nil {
		return cachedToolResult{}, err
	}

	prepared := cachedToolResult{
		Content:     content,
		ArtifactID:  artifactID,
		OutputBytes: plan.execution.outputBytes,
	}
	if plan.cacheable && plan.canonicalKey != "" {
		planCtx.toolCache.store(plan.canonicalKey, prepared)
	}

	plan.execution.finalized = true
	plan.execution.finalResult = prepared
	return prepared, nil
}

func toolPlanDurationMs(plan *plannedToolCall) int64 {
	if plan == nil || plan.execution == nil {
		return 0
	}
	return plan.execution.durationMs
}

func buildToolCallPlan(planCtx toolPlanningContext, toolCall openai.ToolCall) plannedToolCall {
	plan := plannedToolCall{
		toolCall:     toolCall,
		toolName:     toolCall.Function.Name,
		logArguments: sanitizeToolArguments(toolCall.Function.Arguments),
	}
	if _, ok := concurrentReadOnlyTools[plan.toolName]; ok {
		plan.concurrent = true
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(plan.logArguments), &args); err != nil {
		plan.immediateResult = fmt.Sprintf("Error parsing JSON arguments: %v", err)
		return plan
	}

	switch plan.toolName {
	case "read_file":
		path := GetStringArg(args, "path")
		startLine := int(GetFloatArg(args, "start_line"))
		endLine := int(GetFloatArg(args, "end_line"))
		maxOutputBytes := int(GetFloatArg(args, "max_output_bytes"))
		resolvedPath, err := resolveToolPath(planCtx.task.BasePath, path)
		if err != nil {
			plan.immediateResult = err.Error()
			return plan
		}
		autoRange := startLine <= 0 && endLine <= 0
		if startLine < 1 {
			startLine = 1
		}
		if endLine <= 0 && !autoRange {
			endLine = startLine + 1000
		}
		if autoRange {
			startLine = 1
			endLine = defaultReadMaxLines
		}
		if maxOutputBytes <= 0 {
			maxOutputBytes = defaultReadMaxOutputBytes
		}
		if startLine > 0 && endLine > 0 && startLine > endLine {
			plan.immediateResult = fmt.Sprintf("Error: start_line (%d) > end_line (%d)", startLine, endLine)
			return plan
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			Path           string `json:"path"`
			StartLine      int    `json:"start_line"`
			EndLine        int    `json:"end_line"`
			MaxOutputBytes int    `json:"max_output_bytes"`
			AutoRange      bool   `json:"auto_range"`
		}{
			Path:           resolvedPath,
			StartLine:      startLine,
			EndLine:        endLine,
			MaxOutputBytes: maxOutputBytes,
			AutoRange:      autoRange,
		})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}

		rawStartLine := int(GetFloatArg(args, "start_line"))
		rawEndLine := int(GetFloatArg(args, "end_line"))
		rawMaxOutputBytes := int(GetFloatArg(args, "max_output_bytes"))
		plan.readFileArtifactRef = &readFileArtifactPlan{
			resolvedPath: resolvedPath,
			startLine:    rawStartLine,
			endLine:      rawEndLine,
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				return ExecuteReadFile(resolvedPath, rawStartLine, rawEndLine, rawMaxOutputBytes)
			},
		}

	case "get_evidence":
		evidenceID := strings.TrimSpace(GetStringArg(args, "evidence_id"))
		if evidenceID == "" {
			plan.immediateResult = "Error: evidence_id is required"
			return plan
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			EvidenceID string `json:"evidence_id"`
		}{EvidenceID: evidenceID})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				record, ok := planCtx.session.loadArtifact(evidenceID)
				if !ok {
					return fmt.Sprintf("Error: evidence_id %q not found in the current run.", evidenceID)
				}
				return formatArtifactPayload(record)
			},
		}

	case "get_artifact":
		artifactID := strings.TrimSpace(GetStringArg(args, "artifact_id"))
		if artifactID == "" {
			plan.immediateResult = "Error: artifact_id is required"
			return plan
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			ArtifactID string `json:"artifact_id"`
		}{ArtifactID: artifactID})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				record, ok := planCtx.session.loadArtifact(artifactID)
				if !ok {
					return fmt.Sprintf("Error: artifact_id %q not found in the current run.", artifactID)
				}
				return formatArtifactPayload(record)
			},
		}

	case "query_manifest":
		stage := strings.ToLower(strings.TrimSpace(GetStringArg(args, "stage")))
		module := strings.ToLower(strings.TrimSpace(GetStringArg(args, "module")))
		ruleName := strings.ToLower(strings.TrimSpace(GetStringArg(args, "rule_name")))
		offset := int(GetFloatArg(args, "offset"))
		limit := int(GetFloatArg(args, "limit"))
		if offset < 0 {
			offset = 0
		}
		if limit <= 0 {
			limit = manifestDefaultQueryLimit
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			Stage    string `json:"stage"`
			Module   string `json:"module"`
			RuleName string `json:"rule_name"`
			Offset   int    `json:"offset"`
			Limit    int    `json:"limit"`
		}{
			Stage:    stage,
			Module:   module,
			RuleName: ruleName,
			Offset:   offset,
			Limit:    limit,
		})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				return ExecuteQueryManifest(planCtx.task, stage, module, ruleName, offset, limit)
			},
		}

	case "query_routes":
		source := strings.ToLower(strings.TrimSpace(GetStringArg(args, "source")))
		pathFilter := strings.ToLower(strings.TrimSpace(GetStringArg(args, "path")))
		method := strings.ToUpper(strings.TrimSpace(GetStringArg(args, "method")))
		offset := int(GetFloatArg(args, "offset"))
		limit := int(GetFloatArg(args, "limit"))
		if offset < 0 {
			offset = 0
		}
		if limit <= 0 {
			limit = manifestDefaultQueryLimit
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			Source string `json:"source"`
			Path   string `json:"path"`
			Method string `json:"method"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}{
			Source: source,
			Path:   pathFilter,
			Method: method,
			Offset: offset,
			Limit:  limit,
		})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				return ExecuteQueryRoutes(planCtx.task, source, pathFilter, method, offset, limit)
			},
		}

	case "query_stage_output":
		stageName := strings.TrimSpace(GetStringArg(args, "stage"))
		if stageName == "" && planCtx.currentStage != nil {
			stageName = planCtx.currentStage.Name
		}
		if stageName == "" {
			plan.immediateResult = "Error: stage is required when no current stage context is available."
			return plan
		}
		origin := strings.ToLower(strings.TrimSpace(GetStringArg(args, "origin")))
		verificationStatus := strings.ToLower(strings.TrimSpace(GetStringArg(args, "verification_status")))
		offset := int(GetFloatArg(args, "offset"))
		limit := int(GetFloatArg(args, "limit"))
		if offset < 0 {
			offset = 0
		}
		if limit <= 0 {
			limit = manifestDefaultQueryLimit
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			Stage              string `json:"stage"`
			Origin             string `json:"origin"`
			VerificationStatus string `json:"verification_status"`
			Offset             int    `json:"offset"`
			Limit              int    `json:"limit"`
		}{
			Stage:              stageName,
			Origin:             origin,
			VerificationStatus: verificationStatus,
			Offset:             offset,
			Limit:              limit,
		})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				return ExecuteQueryStageOutput(planCtx.task, planCtx.currentStage, stageName, origin, verificationStatus, offset, limit)
			},
		}

	case "list_files":
		path := GetStringArg(args, "path")
		maxEntries := int(GetFloatArg(args, "max_entries"))
		resolvedPath, err := resolveToolPath(planCtx.task.BasePath, path)
		if err != nil {
			plan.immediateResult = err.Error()
			return plan
		}
		if maxEntries <= 0 {
			maxEntries = defaultListMaxEntries
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			Path       string `json:"path"`
			MaxEntries int    `json:"max_entries"`
		}{
			Path:       resolvedPath,
			MaxEntries: maxEntries,
		})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				return ExecuteListFiles(resolvedPath, maxEntries)
			},
		}

	case "list_dir_tree":
		path := GetStringArg(args, "path")
		maxDepth := int(GetFloatArg(args, "max_depth"))
		maxEntries := int(GetFloatArg(args, "max_entries"))
		resolvedPath, err := resolveToolPath(planCtx.task.BasePath, path)
		if err != nil {
			plan.immediateResult = err.Error()
			return plan
		}
		if maxDepth == 0 {
			maxDepth = 2
		}
		if maxEntries <= 0 {
			maxEntries = defaultTreeMaxEntries
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			Path       string `json:"path"`
			MaxDepth   int    `json:"max_depth"`
			MaxEntries int    `json:"max_entries"`
		}{
			Path:       resolvedPath,
			MaxDepth:   maxDepth,
			MaxEntries: maxEntries,
		})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				return ExecuteListDirTree(resolvedPath, maxDepth, maxEntries)
			},
		}

	case "search_files":
		path := GetStringArg(args, "path")
		pattern := GetStringArg(args, "pattern")
		maxResults := int(GetFloatArg(args, "max_results"))
		offset := int(GetFloatArg(args, "offset"))
		resolvedPath, err := resolveToolPath(planCtx.task.BasePath, path)
		if err != nil {
			plan.immediateResult = err.Error()
			return plan
		}
		if maxResults <= 0 {
			maxResults = defaultSearchMaxResults
		}
		if offset < 0 {
			offset = 0
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			Path       string `json:"path"`
			Pattern    string `json:"pattern"`
			MaxResults int    `json:"max_results"`
			Offset     int    `json:"offset"`
		}{
			Path:       resolvedPath,
			Pattern:    pattern,
			MaxResults: maxResults,
			Offset:     offset,
		})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				return ExecuteSearchFiles(resolvedPath, pattern, maxResults, offset)
			},
		}

	case "grep_files":
		path := GetStringArg(args, "path")
		pattern := GetStringArg(args, "pattern")
		caseInsensitive := false
		if v, ok := args["case_insensitive"]; ok {
			if b, ok := v.(bool); ok {
				caseInsensitive = b
			}
		}
		maxResults := int(GetFloatArg(args, "max_results"))
		offset := int(GetFloatArg(args, "offset"))
		maxFiles := int(GetFloatArg(args, "max_files"))
		maxOutputBytes := int(GetFloatArg(args, "max_output_bytes"))
		resolvedPath, err := resolveToolPath(planCtx.task.BasePath, path)
		if err != nil {
			plan.immediateResult = err.Error()
			return plan
		}
		if maxResults <= 0 {
			maxResults = defaultGrepMaxResults
		}
		if offset < 0 {
			offset = 0
		}
		if maxOutputBytes <= 0 {
			maxOutputBytes = defaultGrepMaxOutputBytes
		}
		prefix := ""
		if caseInsensitive {
			prefix = "(?i)"
		}
		if _, compileErr := regexp.Compile(prefix + pattern); compileErr != nil {
			plan.immediateResult = fmt.Sprintf("Invalid regex pattern: %v", compileErr)
			return plan
		}

		plan.cacheable = true
		plan.canonicalKey = canonicalToolKey(plan.toolName, struct {
			Path            string `json:"path"`
			Pattern         string `json:"pattern"`
			CaseInsensitive bool   `json:"case_insensitive"`
			MaxResults      int    `json:"max_results"`
			Offset          int    `json:"offset"`
			MaxFiles        int    `json:"max_files"`
			MaxOutputBytes  int    `json:"max_output_bytes"`
		}{
			Path:            resolvedPath,
			Pattern:         pattern,
			CaseInsensitive: caseInsensitive,
			MaxResults:      maxResults,
			Offset:          offset,
			MaxFiles:        maxFiles,
			MaxOutputBytes:  maxOutputBytes,
		})
		if cached, ok := planCtx.toolCache.load(plan.canonicalKey); ok {
			plan.hasCachedResult = true
			plan.cachedResult = cached
			return plan
		}
		plan.execution = &sharedToolExecution{
			toolName: plan.toolName,
			execute: func() string {
				return ExecuteGrepFiles(resolvedPath, pattern, caseInsensitive, maxResults, offset, maxFiles, maxOutputBytes)
			},
		}

	default:
		plan.immediateResult = fmt.Sprintf("Error: Unknown tool %s", plan.toolName)
	}

	return plan
}

func canonicalToolKey(toolName string, payload any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return toolName
	}
	return toolName + "|" + string(data)
}

func sanitizeToolArguments(raw string) string {
	argStr := strings.TrimSpace(raw)
	if !strings.HasPrefix(argStr, "{") {
		if idx := strings.Index(argStr, "{"); idx != -1 {
			argStr = argStr[idx:]
		}
	}
	if !strings.HasSuffix(argStr, "}") {
		if idx := strings.LastIndex(argStr, "}"); idx != -1 {
			argStr = argStr[:idx+1]
		}
	}
	return argStr
}
