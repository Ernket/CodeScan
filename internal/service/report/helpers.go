package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codescan/internal/model"
)

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
			Tone:  strings.ToLower(label),
		})
	}
	return result
}

func locationFile(raw any) string {
	location, ok := raw.(map[string]any)
	if !ok || location == nil {
		return ""
	}
	return strings.TrimSpace(extractString(location["file"]))
}

func formatLocation(raw any) string {
	location, ok := raw.(map[string]any)
	if !ok || location == nil {
		return ""
	}

	file := extractString(location["file"])
	line := extractString(location["line"])
	if file == "" {
		return ""
	}
	if line == "" {
		return file
	}
	return fmt.Sprintf("%s:%s", file, line)
}

func formatTrigger(raw any) (string, string) {
	trigger, ok := raw.(map[string]any)
	if !ok || trigger == nil {
		return "", ""
	}

	method := extractString(trigger["method"])
	path := extractString(trigger["path"])
	parameter := extractString(trigger["parameter"])
	label := strings.TrimSpace(strings.TrimSpace(method) + " " + strings.TrimSpace(path))
	return label, parameter
}

func componentSummary(finding map[string]any) string {
	name := extractString(finding["component_name"])
	version := extractString(finding["component_version"])
	if name == "" {
		return ""
	}
	if version == "" {
		return name
	}
	return fmt.Sprintf("%s @ %s", name, version)
}

func formatValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			part := strings.TrimSpace(formatValue(item))
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		blob, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return ""
		}
		return string(blob)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func extractString(value any) string {
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

func prettyLabel(key string) string {
	key = strings.ReplaceAll(key, "_", " ")
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	parts := strings.Fields(strings.ToLower(key))
	for index, part := range parts {
		parts[index] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func shouldUseCodeBlock(key, value string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if strings.Contains(value, "\n") {
		return true
	}
	switch key {
	case "poc", "poc_http", "vulnerable_code", "execution_logic", "trigger_steps", "impact", "affected_endpoints", "validation_logic", "payload_hint", "upgrade_recommendation", "exposure_mechanism", "reproduction_steps", "manipulated_fields", "preconditions", "state_transition", "race_window", "authorization_logic", "bypass_vector", "session_artifact", "expected_execution":
		return true
	default:
		return len(value) > 140
	}
}

func fileNameForTask(task model.Task, generatedAt time.Time) string {
	base := strings.TrimSpace(task.Name)
	if base == "" {
		base = "codescan-task"
	}
	base = sanitizeFileName(base)
	if base == "" {
		base = "codescan-task"
	}
	return fmt.Sprintf("%s-report-%s.html", base, generatedAt.Format("20060102-150405"))
}

func sanitizeFileName(input string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(input) {
		switch {
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r == '-' || r == '_' || r == ' ':
			builder.WriteRune('-')
		default:
			builder.WriteRune('-')
		}
	}
	name := strings.Trim(builder.String(), "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func formatTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return "暂无"
	}
	return ts.Local().Format("2006-01-02 15:04:05")
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
