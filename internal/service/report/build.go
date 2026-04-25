package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"codescan/internal/model"
	summarysvc "codescan/internal/service/summary"
)

func Build(task model.Task, generatedAt time.Time) (*TaskReport, error) {
	report := &TaskReport{
		Task: TaskSummary{
			ID:          task.ID,
			ShortID:     shortID(task.ID),
			Name:        fallbackString(task.Name, "未命名任务"),
			Remark:      strings.TrimSpace(task.Remark),
			CreatedAt:   formatTimestamp(task.CreatedAt),
			GeneratedAt: formatTimestamp(generatedAt),
			FileName:    fileNameForTask(task, generatedAt),
		},
	}

	stageByKey := make(map[string]model.TaskStage, len(task.Stages))
	for _, stage := range task.Stages {
		stageByKey[stage.Name] = stage
	}

	totalFiles := map[string]struct{}{}
	totalInterfaces := map[string]struct{}{}
	totalSeverity := map[string]int{}
	routeCount := summarysvc.ParseRouteCount(task.OutputJSON, task.Result)
	cleanStageCount := 0
	totalFindings := 0

	for _, cfg := range stageConfigs {
		stage, ok := stageByKey[cfg.Key]
		if !ok || !strings.EqualFold(stage.Status, "completed") {
			continue
		}

		reportStage, include := buildStageReport(cfg, stage)
		if !include {
			continue
		}

		report.Stages = append(report.Stages, reportStage)
		totalFindings += reportStage.FindingCount
		if reportStage.ZeroFindings {
			cleanStageCount++
		}
		for _, finding := range reportStage.Findings {
			if finding.LocationFile != "" {
				totalFiles[finding.LocationFile] = struct{}{}
			}
			if finding.Trigger != "" {
				totalInterfaces[finding.Trigger] = struct{}{}
			}
			totalSeverity[finding.Severity]++
		}
	}

	if len(report.Stages) == 0 {
		return nil, ErrNoExportableStages
	}

	report.Summary = ReportSummary{
		CompletedStageCount: len(report.Stages),
		CleanStageCount:     cleanStageCount,
		TotalFindings:       totalFindings,
		UniqueFiles:         len(totalFiles),
		UniqueInterfaces:    len(totalInterfaces),
		RouteCount:          routeCount,
		Severities:          severityBreakdown(totalSeverity),
	}
	if report.Summary.RouteCount > 0 && report.Summary.UniqueInterfaces < report.Summary.RouteCount {
		report.Summary.UniqueInterfaces = report.Summary.RouteCount
	}

	return report, nil
}

func buildStageReport(cfg stageConfig, stage model.TaskStage) (ReportStage, bool) {
	stageReport := ReportStage{
		Key:         cfg.Key,
		Label:       cfg.Label,
		Description: cfg.Description,
		Accent:      cfg.Accent,
		CompletedAt: formatTimestamp(stage.UpdatedAt),
	}

	findings, rawResult, ok := summarysvc.ParseFindings(stage.OutputJSON, stage.Result)
	if !ok {
		return ReportStage{}, false
	}

	if findings == nil {
		stageReport.RawOnly = true
		stageReport.RawResult = rawResult
		stageReport.SummaryText = "该阶段已完成，当前以原始 AI 输出形式导出。"
		return stageReport, true
	}

	activeFindings := summarysvc.ActiveFindings(findings)
	rejectedFindings := summarysvc.RejectedFindings(findings)
	stageReport.FindingCount = len(activeFindings)
	stageReport.RejectedCount = len(rejectedFindings)
	stageReport.AllRejected = stageReport.FindingCount == 0 && stageReport.RejectedCount > 0
	stageReport.ZeroFindings = stageReport.FindingCount == 0 && !stageReport.AllRejected
	if stageReport.AllRejected {
		stageReport.SummaryText = "当前发现已在复核过程中全部被驳回。"
	} else if stageReport.ZeroFindings {
		stageReport.SummaryText = "该阶段已完成，未发现已确认漏洞。"
	} else {
		stageReport.SummaryText = formatFindingSummary(len(activeFindings))
	}

	files := map[string]struct{}{}
	interfaces := map[string]struct{}{}
	severityCounts := map[string]int{}
	for index, finding := range activeFindings {
		reportFinding := buildFindingReport(cfg, finding, index)
		stageReport.Findings = append(stageReport.Findings, reportFinding)
		if reportFinding.LocationFile != "" {
			files[reportFinding.LocationFile] = struct{}{}
		}
		if reportFinding.Trigger != "" {
			interfaces[reportFinding.Trigger] = struct{}{}
		}
		severityCounts[reportFinding.Severity]++
	}
	for index, finding := range rejectedFindings {
		stageReport.RejectedFindings = append(stageReport.RejectedFindings, buildFindingReport(cfg, finding, index))
	}

	stageReport.UniqueFiles = len(files)
	stageReport.UniqueInterfaces = len(interfaces)
	stageReport.SeverityBreakdown = severityBreakdown(severityCounts)

	return stageReport, true
}

func buildFindingReport(cfg stageConfig, finding map[string]any, index int) ReportFinding {
	severity := summarysvc.EffectiveSeverity(finding)
	trigger, triggerParameter := formatTrigger(finding["trigger"])
	reportFinding := ReportFinding{
		Anchor:           fmt.Sprintf("%s-%d", cfg.Key, index+1),
		Severity:         severity,
		SeverityTone:     strings.ToLower(severity),
		Verification:     summarysvc.FindingVerificationStatus(finding),
		ReviewReason:     strings.TrimSpace(summarysvc.ExtractString(finding["verification_reason"])),
		Origin:           strings.TrimSpace(summarysvc.ExtractString(finding["origin"])),
		Subtype:          fallbackString(summarysvc.ExtractString(finding["subtype"]), cfg.Label),
		Description:      fallbackString(summarysvc.ExtractString(finding["description"]), "暂无描述。"),
		LocationFile:     locationFile(finding["location"]),
		Location:         formatLocation(finding["location"]),
		Trigger:          trigger,
		TriggerParameter: triggerParameter,
	}

	usedKeys := map[string]bool{
		"type":                true,
		"subtype":             true,
		"description":         true,
		"severity":            true,
		"reviewed_severity":   true,
		"verification_status": true,
		"verification_reason": true,
		"origin":              true,
		"location":            true,
		"trigger":             true,
	}

	addDetail := func(label, value string, code bool) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		reportFinding.DetailFields = append(reportFinding.DetailFields, DisplayField{
			Label: label,
			Value: value,
			Code:  code,
		})
	}

	if cfg.Key == "config" {
		componentLabel := componentSummary(finding)
		if componentLabel != "" {
			addDetail("组件", componentLabel, false)
			usedKeys["component_name"] = true
			usedKeys["component_version"] = true
		}
	}

	for _, field := range cfg.Fields {
		value := formatValue(finding[field.Key])
		if value == "" {
			continue
		}
		addDetail(field.Label, value, field.Code)
		usedKeys[field.Key] = true
	}

	var leftoverKeys []string
	for key, value := range finding {
		if usedKeys[key] {
			continue
		}
		if strings.TrimSpace(formatValue(value)) == "" {
			continue
		}
		leftoverKeys = append(leftoverKeys, key)
	}
	sort.Strings(leftoverKeys)
	for _, key := range leftoverKeys {
		value := formatValue(finding[key])
		addDetail(prettyLabel(key), value, shouldUseCodeBlock(key, value))
	}

	return reportFinding
}

func formatFindingSummary(count int) string {
	return fmt.Sprintf("本报告纳入 %d 条发现。", count)
}
