package scanner

import (
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
