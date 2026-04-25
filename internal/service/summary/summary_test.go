package summary

import (
	"encoding/json"
	"testing"
	"time"

	"codescan/internal/model"
)

func TestBuildStatsAggregatesDashboardMetrics(t *testing.T) {
	tasks := []model.Task{
		{
			ID:        "task-1",
			Status:    "completed",
			CreatedAt: time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC),
			OutputJSON: json.RawMessage(`[
				{"method":"GET","path":"/api/users"},
				{"method":"POST","path":"/api/login"}
			]`),
			Stages: []model.TaskStage{
				{
					Name:       "rce",
					Status:     "completed",
					OutputJSON: json.RawMessage(`[{"severity":"critical","type":"RCE"}]`),
				},
				{
					Name:       "auth",
					Status:     "completed",
					OutputJSON: json.RawMessage(`[]`),
				},
			},
		},
		{
			ID:        "task-2",
			Status:    "running",
			CreatedAt: time.Date(2026, 3, 31, 11, 0, 0, 0, time.UTC),
			Result:    "```json\n[{\"method\":\"DELETE\",\"path\":\"/api/orders/1\"}]\n```",
			Stages: []model.TaskStage{
				{
					Name:   "injection",
					Status: "completed",
					Result: "manual note: suspicious SQL behavior but not structured",
				},
				{
					Name:       "xss",
					Status:     "running",
					OutputJSON: json.RawMessage(`[{"severity":"high","type":"XSS"}]`),
				},
			},
		},
	}

	stats := BuildStats(tasks)

	if stats.Projects != 2 {
		t.Fatalf("expected 2 projects, got %d", stats.Projects)
	}
	if stats.Interfaces != 3 {
		t.Fatalf("expected 3 interfaces, got %d", stats.Interfaces)
	}
	if stats.Vulns != 1 {
		t.Fatalf("expected 1 vulnerability, got %d", stats.Vulns)
	}
	if stats.CompletedAudits != 3 {
		t.Fatalf("expected 3 completed audits, got %d", stats.CompletedAudits)
	}
	if stats.StatusBreakdown.Completed != 1 || stats.StatusBreakdown.Running != 1 {
		t.Fatalf("unexpected status breakdown: %+v", stats.StatusBreakdown)
	}
	if len(stats.SeverityBreakdown) != 1 || stats.SeverityBreakdown[0].Label != "CRITICAL" || stats.SeverityBreakdown[0].Count != 1 {
		t.Fatalf("unexpected severity breakdown: %+v", stats.SeverityBreakdown)
	}
	if got := stageCount(stats.StageBreakdown, "rce"); got != 1 {
		t.Fatalf("expected rce completed count 1, got %d", got)
	}
	if got := stageCount(stats.StageBreakdown, "auth"); got != 1 {
		t.Fatalf("expected auth completed count 1, got %d", got)
	}
	if got := stageCount(stats.StageBreakdown, "injection"); got != 1 {
		t.Fatalf("expected injection completed count 1, got %d", got)
	}
	if got := stageCount(stats.StageBreakdown, "xss"); got != 0 {
		t.Fatalf("expected xss completed count 0, got %d", got)
	}
}

func TestBuildTaskListItemTracksSeverityAndCoverage(t *testing.T) {
	item := BuildTaskListItem(model.Task{
		ID:        "task-3",
		Name:      "demo",
		Remark:    "summary",
		Status:    "paused",
		CreatedAt: time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC),
		OutputJSON: json.RawMessage(`[
			{"method":"GET","path":"/health"},
			{"method":"POST","path":"/api/login"}
		]`),
		Stages: []model.TaskStage{
			{
				Name:       "xss",
				Status:     "completed",
				OutputJSON: json.RawMessage(`[{"severity":"medium","type":"XSS"},{"severity":"high","type":"XSS"}]`),
			},
			{
				Name:       "logic",
				Status:     "completed",
				OutputJSON: json.RawMessage(`[]`),
			},
		},
	})

	if item.RouteCount != 2 {
		t.Fatalf("expected 2 routes, got %d", item.RouteCount)
	}
	if item.FindingCount != 2 {
		t.Fatalf("expected 2 findings, got %d", item.FindingCount)
	}
	if item.CompletedStageCount != 2 {
		t.Fatalf("expected completed stage count 2, got %d", item.CompletedStageCount)
	}
	if item.TotalStageCount != KnownStageCount() {
		t.Fatalf("expected total stage count %d, got %d", KnownStageCount(), item.TotalStageCount)
	}
	if item.HighestSeverity != "HIGH" {
		t.Fatalf("expected highest severity HIGH, got %s", item.HighestSeverity)
	}
}

func TestBuildTaskListItemMarksRawOnlyStagesUnknown(t *testing.T) {
	item := BuildTaskListItem(model.Task{
		ID:        "task-4",
		Status:    "failed",
		CreatedAt: time.Date(2026, 3, 31, 13, 0, 0, 0, time.UTC),
		Stages: []model.TaskStage{
			{
				Name:   "config",
				Status: "completed",
				Result: "AI output exists but was not repaired into JSON",
			},
		},
	})

	if item.FindingCount != 0 {
		t.Fatalf("expected 0 findings, got %d", item.FindingCount)
	}
	if item.CompletedStageCount != 1 {
		t.Fatalf("expected completed stage count 1, got %d", item.CompletedStageCount)
	}
	if item.HighestSeverity != "UNKNOWN" {
		t.Fatalf("expected highest severity UNKNOWN, got %s", item.HighestSeverity)
	}
}

func TestBuildTaskListItemMarksActiveUnknownSeverity(t *testing.T) {
	item := BuildTaskListItem(model.Task{
		ID:        "task-5",
		Status:    "completed",
		CreatedAt: time.Date(2026, 3, 31, 14, 0, 0, 0, time.UTC),
		Stages: []model.TaskStage{
			{
				Name:       "auth",
				Status:     "completed",
				OutputJSON: json.RawMessage(`[{"type":"Authentication","severity":"mystery"}]`),
			},
		},
	})

	if item.FindingCount != 1 {
		t.Fatalf("expected 1 finding, got %d", item.FindingCount)
	}
	if item.HighestSeverity != "UNKNOWN" {
		t.Fatalf("expected highest severity UNKNOWN, got %s", item.HighestSeverity)
	}
}

func TestBuildStatsIncludesUnknownSeverityBreakdown(t *testing.T) {
	stats := BuildStats([]model.Task{
		{
			ID:        "task-6",
			Status:    "completed",
			CreatedAt: time.Date(2026, 3, 31, 15, 0, 0, 0, time.UTC),
			Stages: []model.TaskStage{
				{
					Name:   "logic",
					Status: "completed",
					OutputJSON: json.RawMessage(`[
						{"type":"BusinessLogic","severity":"info"},
						{"type":"BusinessLogic","severity":"mystery"}
					]`),
				},
			},
		},
	})

	if len(stats.SeverityBreakdown) != 2 {
		t.Fatalf("expected 2 severity buckets, got %+v", stats.SeverityBreakdown)
	}
	if stats.SeverityBreakdown[0].Label != "INFO" || stats.SeverityBreakdown[0].Count != 1 {
		t.Fatalf("expected INFO bucket first, got %+v", stats.SeverityBreakdown)
	}
	if stats.SeverityBreakdown[1].Label != "UNKNOWN" || stats.SeverityBreakdown[1].Count != 1 {
		t.Fatalf("expected UNKNOWN bucket after INFO, got %+v", stats.SeverityBreakdown)
	}
}

func TestNormalizeSeverityReturnsUnknownForMissingOrUnexpectedValues(t *testing.T) {
	cases := []string{"", "mystery", "  "}
	for _, input := range cases {
		if got := NormalizeSeverity(input); got != "UNKNOWN" {
			t.Fatalf("expected UNKNOWN for %q, got %s", input, got)
		}
	}
}

func TestParseFindingsAndRouteCountSupportFallbackJSON(t *testing.T) {
	findings, rawOnly, ok := ParseFindings(nil, "prefix ```json\n[{\"severity\":\"low\"}]\n``` suffix")
	if !ok {
		t.Fatal("expected findings to parse from fallback JSON block")
	}
	if rawOnly != "" {
		t.Fatalf("expected structured findings, got raw-only %q", rawOnly)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if got := ParseRouteCount(nil, "routes: [{\"method\":\"GET\",\"path\":\"/api/test\"}]"); got != 1 {
		t.Fatalf("expected 1 route from fallback, got %d", got)
	}
}

func stageCount(items []StageCount, key string) int {
	for _, item := range items {
		if item.Key == key {
			return item.Count
		}
	}
	return -1
}
