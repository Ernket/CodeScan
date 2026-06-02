package scanner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	summarysvc "codescan/internal/service/summary"
)

type repairStageRule struct {
	findingType    string
	requiredFields []string
	arrayFields    []string
}

var repairStageOrder = []string{
	"init",
	"rce",
	"injection",
	"auth",
	"access",
	"xss",
	"config",
	"fileop",
	"logic",
}

var stageFindingTypes = map[string]string{
	"rce":       "RCE",
	"injection": "Injection",
	"auth":      "Authentication",
	"access":    "Authorization",
	"xss":       "XSS",
	"config":    "Configuration",
	"fileop":    "FileOperation",
	"logic":     "BusinessLogic",
}

var repairStageRules = map[string]repairStageRule{
	"init": {
		requiredFields: []string{"method", "path", "source", "description"},
	},
	"rce": {
		findingType: stageFindingTypes["rce"],
	},
	"injection": {
		findingType: stageFindingTypes["injection"],
	},
	"auth": {
		findingType: stageFindingTypes["auth"],
		arrayFields: []string{"affected_endpoints"},
	},
	"access": {
		findingType: stageFindingTypes["access"],
		arrayFields: []string{"affected_endpoints"},
	},
	"xss": {
		findingType: stageFindingTypes["xss"],
	},
	"config": {
		findingType: stageFindingTypes["config"],
		arrayFields: []string{"affected_endpoints"},
	},
	"fileop": {
		findingType: stageFindingTypes["fileop"],
	},
	"logic": {
		findingType: stageFindingTypes["logic"],
		arrayFields: []string{"affected_endpoints", "manipulated_fields"},
	},
}

func NormalizeRepairStage(stage string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(stage))
	if normalized == "" {
		normalized = "init"
	}
	_, ok := repairStageRules[normalized]
	return normalized, ok
}

func IsSupportedRepairStage(stage string) bool {
	_, ok := NormalizeRepairStage(stage)
	return ok
}

func SupportedRepairStages() []string {
	out := make([]string, len(repairStageOrder))
	copy(out, repairStageOrder)
	return out
}

func ParseValidatedRepairJSON(raw string, stage string) ([]map[string]any, json.RawMessage, error) {
	stage, ok := NormalizeRepairStage(stage)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported repair stage: %s", stage)
	}

	jsonPart := strings.TrimSpace(extractJSON(raw))
	if !json.Valid([]byte(jsonPart)) {
		return nil, nil, fmt.Errorf("stage %q output is not valid JSON", stage)
	}
	if !strings.HasPrefix(jsonPart, "[") {
		return nil, nil, fmt.Errorf("stage %q output must be a JSON array", stage)
	}

	decoder := json.NewDecoder(strings.NewReader(jsonPart))
	decoder.UseNumber()
	var items []map[string]any
	if err := decoder.Decode(&items); err != nil {
		return nil, nil, fmt.Errorf("stage %q output must be a JSON array: %w", stage, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, nil, fmt.Errorf("stage %q output contains invalid trailing JSON data: %w", stage, err)
		}
		return nil, nil, fmt.Errorf("stage %q output contains trailing JSON data", stage)
	}
	if err := ValidateRepairJSONItems(stage, items); err != nil {
		return nil, nil, err
	}

	return items, json.RawMessage(bytes.TrimSpace([]byte(jsonPart))), nil
}

func ValidateRepairJSONItems(stage string, items []map[string]any) error {
	stage, ok := NormalizeRepairStage(stage)
	if !ok {
		return fmt.Errorf("unsupported repair stage: %s", stage)
	}

	rule := repairStageRules[stage]
	for i, item := range items {
		if item == nil {
			return fmt.Errorf("stage %q item %d must be a JSON object", stage, i)
		}

		for _, field := range rule.requiredFields {
			if _, exists := item[field]; !exists {
				return fmt.Errorf("stage %q item %d missing required field %q", stage, i, field)
			}
		}

		if rule.findingType != "" {
			got := strings.TrimSpace(summarysvc.ExtractString(item["type"]))
			if got != rule.findingType {
				return fmt.Errorf("stage %q item %d type must be %q, got %q", stage, i, rule.findingType, got)
			}
		}

		for _, field := range rule.arrayFields {
			value, exists := item[field]
			if !exists {
				return fmt.Errorf("stage %q item %d missing required array field %q", stage, i, field)
			}
			if _, ok := value.([]any); !ok {
				return fmt.Errorf("stage %q item %d field %q must be a JSON array", stage, i, field)
			}
		}
	}

	return nil
}
