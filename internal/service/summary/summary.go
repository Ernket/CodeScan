package summary

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codescan/internal/model"
)

type SeverityCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type StageCount struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type StatusBreakdown struct {
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Paused    int `json:"paused"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

type Stats struct {
	Projects          int             `json:"projects"`
	Interfaces        int             `json:"interfaces"`
	Vulns             int             `json:"vulns"`
	CompletedAudits   int             `json:"completed_audits"`
	StatusBreakdown   StatusBreakdown `json:"status_breakdown"`
	SeverityBreakdown []SeverityCount `json:"severity_breakdown"`
	StageBreakdown    []StageCount    `json:"stage_breakdown"`
}

type TaskListItem struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Remark              string    `json:"remark"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	RouteCount          int       `json:"route_count"`
	FindingCount        int       `json:"finding_count"`
	CompletedStageCount int       `json:"completed_stage_count"`
	TotalStageCount     int       `json:"total_stage_count"`
	HighestSeverity     string    `json:"highest_severity"`
}

type stageDefinition struct {
	Key   string
	Label string
}

var knownStages = []stageDefinition{
	{Key: "rce", Label: "RCE Audit"},
	{Key: "injection", Label: "Injection Audit"},
	{Key: "auth", Label: "Auth & Session Audit"},
	{Key: "access", Label: "Access Control Audit"},
	{Key: "xss", Label: "XSS Audit"},
	{Key: "config", Label: "Config & Component Audit"},
	{Key: "fileop", Label: "File Operation Audit"},
	{Key: "logic", Label: "Business Logic Audit"},
}

var stageLabelByKey = func() map[string]string {
	out := make(map[string]string, len(knownStages))
	for _, stage := range knownStages {
		out[stage.Key] = stage.Label
	}
	return out
}()

var severityRank = map[string]int{
	"CRITICAL": 5,
	"HIGH":     4,
	"MEDIUM":   3,
	"LOW":      2,
	"INFO":     1,
	"UNKNOWN":  0,
	"NONE":     -1,
}

func BuildStats(tasks []model.Task) Stats {
	stats := Stats{
		Projects:          len(tasks),
		SeverityBreakdown: []SeverityCount{},
		StageBreakdown:    make([]StageCount, 0, len(knownStages)),
	}

	severityCounts := map[string]int{}
	stageCounts := map[string]int{}

	for _, task := range tasks {
		stats.Interfaces += ParseRouteCount(task.OutputJSON, task.Result)
		stats.StatusBreakdown = addStatus(stats.StatusBreakdown, task.Status)

		for _, stage := range task.Stages {
			if !isKnownCompletedStage(stage) {
				continue
			}

			stats.CompletedAudits++
			stageCounts[stage.Name]++

			findings, rawOnly, hasPayload := stageFindings(stage)
			if !hasPayload || rawOnly {
				continue
			}

			activeFindings := ActiveFindings(findings)
			stats.Vulns += len(activeFindings)
			for _, finding := range activeFindings {
				severityCounts[EffectiveSeverity(finding)]++
			}
		}
	}

	for _, stage := range knownStages {
		stats.StageBreakdown = append(stats.StageBreakdown, StageCount{
			Key:   stage.Key,
			Label: stage.Label,
			Count: stageCounts[stage.Key],
		})
	}
	stats.SeverityBreakdown = severityBreakdown(severityCounts)

	return stats
}

func BuildTaskList(tasks []model.Task) []TaskListItem {
	items := make([]TaskListItem, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, BuildTaskListItem(task))
	}
	return items
}

func BuildTaskListItem(task model.Task) TaskListItem {
	findingCount := 0
	completedStageCount := 0
	highestSeverity := "NONE"
	rawOnlyCompleted := false

	for _, stage := range task.Stages {
		if !isKnownCompletedStage(stage) {
			continue
		}

		completedStageCount++
		findings, rawOnly, hasPayload := stageFindings(stage)
		if !hasPayload {
			continue
		}
		if rawOnly {
			rawOnlyCompleted = true
			continue
		}

		activeFindings := ActiveFindings(findings)
		findingCount += len(activeFindings)
		for _, finding := range activeFindings {
			severity := EffectiveSeverity(finding)
			if severityRank[severity] > severityRank[highestSeverity] {
				highestSeverity = severity
			}
		}
	}

	if findingCount == 0 {
		if rawOnlyCompleted {
			highestSeverity = "UNKNOWN"
		} else {
			highestSeverity = "NONE"
		}
	}

	return TaskListItem{
		ID:                  task.ID,
		Name:                task.Name,
		Remark:              task.Remark,
		Status:              task.Status,
		CreatedAt:           task.CreatedAt,
		RouteCount:          ParseRouteCount(task.OutputJSON, task.Result),
		FindingCount:        findingCount,
		CompletedStageCount: completedStageCount,
		TotalStageCount:     len(knownStages),
		HighestSeverity:     highestSeverity,
	}
}

func ParseFindings(raw json.RawMessage, fallback string) ([]map[string]any, string, bool) {
	if findings, ok := decodeJSONArray(raw); ok {
		return findings, "", true
	}
	if findings, ok := decodeJSONArray([]byte(ExtractJSONBlock(fallback))); ok {
		return findings, "", true
	}

	trimmed := strings.TrimSpace(fallback)
	if trimmed != "" {
		return nil, trimmed, true
	}

	if trimmedRaw := strings.TrimSpace(string(raw)); trimmedRaw != "" && trimmedRaw != "{}" && trimmedRaw != "null" {
		return nil, trimmedRaw, true
	}

	return nil, "", false
}

func ParseRouteCount(raw json.RawMessage, fallback string) int {
	if routes, ok := decodeJSONArray(raw); ok {
		return len(routes)
	}
	if routes, ok := decodeJSONArray([]byte(ExtractJSONBlock(fallback))); ok {
		return len(routes)
	}
	return 0
}

func ExtractJSONBlock(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	if json.Valid([]byte(input)) {
		return input
	}

	if start := strings.Index(input, "```"); start != -1 {
		end := strings.LastIndex(input, "```")
		if end > start {
			content := strings.TrimSpace(input[start+3 : end])
			content = strings.TrimPrefix(content, "json")
			content = strings.TrimSpace(content)
			if json.Valid([]byte(content)) {
				return content
			}
		}
	}

	if start := strings.Index(input, "["); start != -1 {
		if end := strings.LastIndex(input, "]"); end > start {
			candidate := input[start : end+1]
			if json.Valid([]byte(candidate)) {
				return candidate
			}
		}
	}

	if start := strings.Index(input, "{"); start != -1 {
		if end := strings.LastIndex(input, "}"); end > start {
			candidate := input[start : end+1]
			if json.Valid([]byte(candidate)) {
				return candidate
			}
		}
	}

	return input
}

func NormalizeSeverity(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CRITICAL":
		return "CRITICAL"
	case "HIGH":
		return "HIGH"
	case "MEDIUM":
		return "MEDIUM"
	case "LOW":
		return "LOW"
	case "INFO":
		return "INFO"
	default:
		return "UNKNOWN"
	}
}

func ExtractString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func StageLabel(key string) string {
	return stageLabelByKey[key]
}

func KnownStageCount() int {
	return len(knownStages)
}

func decodeJSONArray(raw []byte) ([]map[string]any, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil, false
	}

	var findings []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &findings); err == nil {
		return findings, true
	}

	return nil, false
}

func addStatus(breakdown StatusBreakdown, status string) StatusBreakdown {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		breakdown.Running++
	case "paused":
		breakdown.Paused++
	case "completed":
		breakdown.Completed++
	case "failed":
		breakdown.Failed++
	default:
		breakdown.Pending++
	}
	return breakdown
}

func isKnownCompletedStage(stage model.TaskStage) bool {
	_, ok := stageLabelByKey[stage.Name]
	return ok && strings.EqualFold(stage.Status, "completed")
}

func stageFindings(stage model.TaskStage) ([]map[string]any, bool, bool) {
	findings, rawResult, ok := ParseFindings(stage.OutputJSON, stage.Result)
	return findings, findings == nil && strings.TrimSpace(rawResult) != "", ok
}

func severityBreakdown(counts map[string]int) []SeverityCount {
	order := []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "INFO", "UNKNOWN"}
	result := make([]SeverityCount, 0, len(order))
	for _, label := range order {
		if counts[label] == 0 {
			continue
		}
		result = append(result, SeverityCount{
			Label: label,
			Count: counts[label],
		})
	}
	return result
}
