package scanner

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
)

func TestBuildToolCallPlanCanonicalizesEquivalentSearchFilesArgs(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	ctx := toolPlanningContext{
		task:      task,
		session:   session,
		toolCache: newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
	}
	first := buildToolCallPlan(ctx, openai.ToolCall{
		Function: openai.FunctionCall{
			Name:      "search_files",
			Arguments: `{"max_results":200,"pattern":"*.go","path":"./src/../src"}`,
		},
	})
	second := buildToolCallPlan(ctx, openai.ToolCall{
		Function: openai.FunctionCall{
			Name:      "search_files",
			Arguments: `{"path":"src","pattern":"*.go"}`,
		},
	})

	if !first.cacheable || !second.cacheable {
		t.Fatalf("expected cacheable plans, got %+v and %+v", first, second)
	}
	if first.canonicalKey == "" || second.canonicalKey == "" {
		t.Fatalf("expected canonical keys, got %q and %q", first.canonicalKey, second.canonicalKey)
	}
	if first.canonicalKey != second.canonicalKey {
		t.Fatalf("expected equivalent calls to share canonical key, got %q vs %q", first.canonicalKey, second.canonicalKey)
	}
}

func TestBuildToolCallPlanDoesNotCacheIgnoredPathErrors(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	ctx := toolPlanningContext{
		task:      task,
		session:   session,
		toolCache: newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
	}
	plan := buildToolCallPlan(ctx, openai.ToolCall{
		Function: openai.FunctionCall{
			Name:      "list_files",
			Arguments: `{"path":"project"}`,
		},
	})

	if plan.cacheable {
		t.Fatalf("expected ignored-path error to be non-cacheable, got %+v", plan)
	}
	if plan.immediateResult == "" || !strings.Contains(plan.immediateResult, "ignored directory") {
		t.Fatalf("expected ignored directory error, got %q", plan.immediateResult)
	}

	ctx.toolCache.mu.RLock()
	entryCount := len(ctx.toolCache.entries)
	ctx.toolCache.mu.RUnlock()
	if entryCount != 0 {
		t.Fatalf("expected cache to remain empty, got %d entries", entryCount)
	}
}

func TestExecuteToolRoundDeduplicatesReadFileArtifacts(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	mustWriteFile(t, filepath.Join(task.BasePath, "alpha.go"), "package main\nfunc main() {}\n")

	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	cache := newToolResultCache(maxToolCacheEntries, maxToolCacheBytes)
	ctx := toolPlanningContext{
		task:      task,
		session:   session,
		toolCache: cache,
	}

	logs := []string{}
	_, err = executeToolRound(ctx, []openai.ToolCall{
		{
			ID: "call-1",
			Function: openai.FunctionCall{
				Name:      "read_file",
				Arguments: `{"path":"alpha.go","start_line":1,"end_line":2}`,
			},
		},
		{
			ID: "call-2",
			Function: openai.FunctionCall{
				Name:      "read_file",
				Arguments: `{"end_line":2,"path":"./alpha.go","start_line":1}`,
			},
		},
	}, func(msg string) {
		logs = append(logs, msg)
	})
	if err != nil {
		t.Fatalf("execute tool round: %v", err)
	}

	if len(session.state.ArtifactOrder) != 1 {
		t.Fatalf("expected one preserved artifact for duplicate read_file calls, got %d", len(session.state.ArtifactOrder))
	}

	toolMessages := 0
	for _, msg := range session.transcript {
		if msg.Role == openai.ChatMessageRoleTool {
			toolMessages++
		}
	}
	if toolMessages != 2 {
		t.Fatalf("expected two tool messages, got %d", toolMessages)
	}
	if !containsAny(strings.Join(logs, "\n"), "Tool round dedupe hit: read_file") {
		t.Fatalf("expected dedupe log, got %v", logs)
	}
}

func TestExecuteToolRoundCachesPreparedLargeReadFileResults(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)

	var builder strings.Builder
	for i := 0; i < 5000; i++ {
		builder.WriteString(strings.Repeat("x", 260))
		builder.WriteString("\n")
	}
	mustWriteFile(t, filepath.Join(task.BasePath, "large.txt"), builder.String())

	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	cache := newToolResultCache(maxToolCacheEntries, maxToolCacheBytes)
	ctx := toolPlanningContext{
		task:      task,
		session:   session,
		toolCache: cache,
	}

	call := openai.ToolCall{
		ID: "call-1",
		Function: openai.FunctionCall{
			Name:      "read_file",
			Arguments: `{"path":"large.txt"}`,
		},
	}
	plan := buildToolCallPlan(ctx, call)
	rawResult := ExecuteReadFile(filepath.Join(task.BasePath, "large.txt"), 0, 0, 0)

	if _, err := executeToolRound(ctx, []openai.ToolCall{call}, func(string) {}); err != nil {
		t.Fatalf("execute tool round: %v", err)
	}

	cache.mu.RLock()
	cached, ok := cache.entries[plan.canonicalKey]
	cache.mu.RUnlock()
	if !ok {
		t.Fatalf("expected cache entry for %q", plan.canonicalKey)
	}
	if cached.ArtifactID == "" {
		t.Fatal("expected cached large read_file result to reference an artifact")
	}
	if len(cached.Content) >= len(rawResult) {
		t.Fatalf("expected cached content to be transcript-safe and shorter than raw output, got %d >= %d", len(cached.Content), len(rawResult))
	}
	if !strings.Contains(cached.Content, cached.ArtifactID) {
		t.Fatalf("expected cached content to reference artifact %s, got %q", cached.ArtifactID, cached.Content)
	}

	cachedPlan := buildToolCallPlan(ctx, call)
	if !cachedPlan.hasCachedResult {
		t.Fatal("expected second equivalent read_file plan to hit cache")
	}
}

func TestSubmitRoutesToolPersistsAndDeduplicatesInitRoutes(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "init", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	ctx := toolPlanningContext{
		task:      task,
		session:   session,
		toolCache: newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
	}

	args := map[string]any{
		"routes": []map[string]any{
			{"method": "get", "path": "/api/users", "source": `routes\user.go`, "description": strings.Repeat("x", maxRouteDescriptionRunes+20)},
			{"method": "GET", "path": "/api/users", "source": "routes/user.go", "description": strings.Repeat("x", maxRouteDescriptionRunes+20)},
			{"method": "post", "path": "/api/login", "source": "routes/auth.go", "description": "login"},
		},
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	_, err = executeToolRound(ctx, []openai.ToolCall{
		{
			ID: "call-submit",
			Function: openai.FunctionCall{
				Name:      "submit_routes",
				Arguments: string(data),
			},
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("execute submit_routes: %v", err)
	}

	routes, err := session.loadSubmittedRoutes()
	if err != nil {
		t.Fatalf("load submitted routes: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("expected two deduped routes, got %+v", routes)
	}
	if got := routes[0]["method"]; got != "GET" {
		t.Fatalf("expected normalized method GET, got %v", got)
	}
	if got := routes[0]["source"]; got != "routes/user.go" {
		t.Fatalf("expected slash-normalized source, got %v", got)
	}
	if len([]rune(routes[0]["description"].(string))) != maxRouteDescriptionRunes {
		t.Fatalf("expected description to be trimmed to %d runes, got %q", maxRouteDescriptionRunes, routes[0]["description"])
	}
}

func TestSubmitRoutesToolRejectsNonInitStage(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	ctx := toolPlanningContext{
		task:      task,
		session:   session,
		toolCache: newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
	}

	plans, err := executeToolRound(ctx, []openai.ToolCall{
		{
			ID: "call-submit",
			Function: openai.FunctionCall{
				Name:      "submit_routes",
				Arguments: `{"routes":[{"method":"GET","path":"/","source":"main.go","description":"root"}]}`,
			},
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("execute submit_routes: %v", err)
	}
	if len(plans) != 1 || !strings.Contains(plans[0].immediateResult, "only available during the init") {
		t.Fatalf("expected non-init rejection, got %+v", plans)
	}
}

func TestSubmitFindingsToolPersistsAndDeduplicatesStageFindings(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	ctx := toolPlanningContext{
		task:      task,
		session:   session,
		toolCache: newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
		kind:      StageRunInitial,
	}

	args := map[string]any{
		"findings": []map[string]any{
			authFinding(1, ""),
			authFinding(1, ""),
			authFinding(2, ""),
		},
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	_, err = executeToolRound(ctx, []openai.ToolCall{
		{
			ID: "call-submit-findings",
			Function: openai.FunctionCall{
				Name:      "submit_findings",
				Arguments: string(data),
			},
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("execute submit_findings: %v", err)
	}

	findings, err := loadSubmittedFindingsForTask(task, "auth")
	if err != nil {
		t.Fatalf("load submitted findings: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected two deduped findings, got %+v", findings)
	}
	if got := findings[0]["type"]; got != "Authentication" {
		t.Fatalf("expected auth finding type, got %v", got)
	}
}

func TestSubmitFindingsToolRejectsInitAndRevalidation(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)

	initSession, err := newScanSession(task, "init", "prompt", true)
	if err != nil {
		t.Fatalf("new init session: %v", err)
	}
	initPlans, err := executeToolRound(toolPlanningContext{
		task:      task,
		session:   initSession,
		toolCache: newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
	}, []openai.ToolCall{
		{
			ID: "call-submit-init",
			Function: openai.FunctionCall{
				Name:      "submit_findings",
				Arguments: `{"findings":[]}`,
			},
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("execute init submit_findings: %v", err)
	}
	if len(initPlans) != 1 || !strings.Contains(initPlans[0].immediateResult, "non-init vulnerability stages") {
		t.Fatalf("expected init rejection, got %+v", initPlans)
	}

	revalidateSession, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new revalidate session: %v", err)
	}
	revalidatePlans, err := executeToolRound(toolPlanningContext{
		task:      task,
		session:   revalidateSession,
		toolCache: newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
		kind:      StageRunRevalidate,
	}, []openai.ToolCall{
		{
			ID: "call-submit-revalidate",
			Function: openai.FunctionCall{
				Name:      "submit_findings",
				Arguments: `{"findings":[]}`,
			},
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("execute revalidate submit_findings: %v", err)
	}
	if len(revalidatePlans) != 1 || !strings.Contains(revalidatePlans[0].immediateResult, "Use submit_reviews instead") {
		t.Fatalf("expected revalidation rejection, got %+v", revalidatePlans)
	}
}

func TestSubmitReviewsToolPersistsAndOverwritesByFindingIndex(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	stage := authStageWithFindings(t, task, authFinding(1, ""), authFinding(2, ""))
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	ctx := toolPlanningContext{
		task:         task,
		currentStage: stage,
		session:      session,
		toolCache:    newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
		kind:         StageRunRevalidate,
	}

	firstArgs := map[string]any{
		"reviews": []map[string]any{
			{"finding_index": 0, "verification_status": summaryStatusConfirmed, "reviewed_severity": "high", "verification_reason": "first review"},
		},
	}
	firstData, err := json.Marshal(firstArgs)
	if err != nil {
		t.Fatalf("marshal first args: %v", err)
	}
	if _, err := executeToolRound(ctx, []openai.ToolCall{{
		ID: "call-submit-review-1",
		Function: openai.FunctionCall{
			Name:      "submit_reviews",
			Arguments: string(firstData),
		},
	}}, func(string) {}); err != nil {
		t.Fatalf("execute first submit_reviews: %v", err)
	}

	secondArgs := map[string]any{
		"reviews": []map[string]any{
			{"finding_index": 0, "verification_status": summaryStatusRejected, "reviewed_severity": "low", "verification_reason": "updated review"},
			{"finding_index": 1, "verification_status": summaryStatusUncertain, "reviewed_severity": "medium", "verification_reason": "second review"},
		},
	}
	secondData, err := json.Marshal(secondArgs)
	if err != nil {
		t.Fatalf("marshal second args: %v", err)
	}
	if _, err := executeToolRound(ctx, []openai.ToolCall{{
		ID: "call-submit-review-2",
		Function: openai.FunctionCall{
			Name:      "submit_reviews",
			Arguments: string(secondData),
		},
	}}, func(string) {}); err != nil {
		t.Fatalf("execute second submit_reviews: %v", err)
	}

	reviews, err := loadSubmittedReviewsForTask(task, "auth")
	if err != nil {
		t.Fatalf("load submitted reviews: %v", err)
	}
	if len(reviews) != 2 {
		t.Fatalf("expected two submitted reviews, got %+v", reviews)
	}
	if got := reviews[0]["verification_status"]; got != summaryStatusRejected {
		t.Fatalf("expected index 0 review to be overwritten, got %+v", reviews[0])
	}
	if got := reviews[0]["reviewed_severity"]; got != "LOW" {
		t.Fatalf("expected normalized reviewed severity LOW, got %v", got)
	}
	if got := reviews[1]["verification_status"]; got != summaryStatusUncertain {
		t.Fatalf("expected index 1 uncertain review, got %+v", reviews[1])
	}
}

func TestSubmitReviewsToolRejectsOutsideRevalidation(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	stage := authStageWithFindings(t, task, authFinding(1, ""))
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	plans, err := executeToolRound(toolPlanningContext{
		task:         task,
		currentStage: stage,
		session:      session,
		toolCache:    newToolResultCache(maxToolCacheEntries, maxToolCacheBytes),
		kind:         StageRunInitial,
	}, []openai.ToolCall{
		{
			ID: "call-submit-review-initial",
			Function: openai.FunctionCall{
				Name:      "submit_reviews",
				Arguments: `{"reviews":[{"finding_index":0,"verification_status":"confirmed","reviewed_severity":"HIGH","verification_reason":"ok"}]}`,
			},
		},
	}, func(string) {})
	if err != nil {
		t.Fatalf("execute submit_reviews outside revalidation: %v", err)
	}
	if len(plans) != 1 || !strings.Contains(plans[0].immediateResult, "only available during revalidation") {
		t.Fatalf("expected non-revalidation rejection, got %+v", plans)
	}
}

func TestRunConcurrentToolExecutionsUsesWorkerPool(t *testing.T) {
	var active int32
	var maxActive int32

	jobs := make([]*sharedToolExecution, 0, toolExecutionParallelism)
	for i := 0; i < toolExecutionParallelism; i++ {
		jobs = append(jobs, &sharedToolExecution{
			toolName: "test",
			execute: func() string {
				current := atomic.AddInt32(&active, 1)
				for {
					observed := atomic.LoadInt32(&maxActive)
					if current <= observed || atomic.CompareAndSwapInt32(&maxActive, observed, current) {
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
				atomic.AddInt32(&active, -1)
				return "ok"
			},
		})
	}

	runConcurrentToolExecutions(jobs)

	if maxActive < 2 {
		t.Fatalf("expected at least two concurrent executions, got %d", maxActive)
	}
	if maxActive > toolExecutionParallelism {
		t.Fatalf("expected worker pool cap %d, got %d", toolExecutionParallelism, maxActive)
	}
}
