package scanner

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"codescan/internal/model"
	summarysvc "codescan/internal/service/summary"
)

type StageRunKind string

const (
	StageRunInitial    StageRunKind = "initial"
	StageRunGapCheck   StageRunKind = "gap_check"
	StageRunRevalidate StageRunKind = "revalidate"
)

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

func normalizeStageRunKind(value string) StageRunKind {
	switch StageRunKind(strings.TrimSpace(strings.ToLower(value))) {
	case StageRunGapCheck:
		return StageRunGapCheck
	case StageRunRevalidate:
		return StageRunRevalidate
	default:
		return StageRunInitial
	}
}

func stageRunKindFromMeta(stage model.TaskStage) StageRunKind {
	return normalizeStageRunKind(stage.Meta.LastRunKind)
}

func stageFindingType(stage string) string {
	return stageFindingTypes[stage]
}

func adaptPromptForRunKind(
	basePrompt string,
	task *model.Task,
	stage string,
	currentStage *model.TaskStage,
	kind StageRunKind,
) (string, error) {
	switch kind {
	case StageRunInitial:
		return basePrompt, nil
	case StageRunGapCheck:
		existing, err := currentJSONArrayForRun(task, currentStage, stage)
		if err != nil {
			return "", err
		}
		if stage == "init" {
			manifest, _ := EnsureProjectManifest(task)
			extra := fmt.Sprintf(`

SUPPLEMENTAL GAP-CHECK MODE:
- The summary below describes the current stored route inventory for this stage.
- You MUST re-review the codebase to find omissions, alternate code paths, overlooked sinks, overlooked route handlers, overlooked boundary cases, and duplicate implementations that were missed in the current result.
- Keep every still-valid existing item.
- Add only newly confirmed items.
- If two items describe the same root issue, keep a single merged item.
- Use query_routes to inspect the current structured route inventory before merging.
- Do NOT add conversational text.
- Final output MUST be the COMPLETE merged JSON array for this stage, not just the delta.

<current_stage_result_summary>
%s
</current_stage_result_summary>`, BuildKnownRoutesContext(task, manifest))
			return basePrompt + extra, nil
		}
		extra := fmt.Sprintf(`

SUPPLEMENTAL GAP-CHECK MODE:
- The summary below describes the current stored findings for this stage.
- You MUST re-review the codebase to find omissions, alternate code paths, overlooked sinks, overlooked route handlers, overlooked boundary cases, and duplicate implementations that were missed in the current result.
- Keep every still-valid existing item.
- Add only newly confirmed items.
- If two items describe the same root issue, keep a single merged item.
- Include an "origin" field on each item using either "initial" or "gap_check".
- Use query_stage_output to inspect the current structured findings before merging.
- Do NOT add conversational text.
- Final output MUST be the COMPLETE merged JSON array for this stage, not just the delta.

<current_stage_result_summary>
%s
</current_stage_result_summary>`, BuildCurrentFindingsContext(stage, existing))
		return basePrompt + extra, nil
	case StageRunRevalidate:
		if stage == "init" {
			return "", fmt.Errorf("route inventory does not support revalidation")
		}
		existing, err := currentJSONArrayForRun(task, currentStage, stage)
		if err != nil {
			return "", err
		}
		manifest, _ := EnsureProjectManifest(task)
		return fmt.Sprintf(`You are a senior security review engineer performing a static revalidation pass for the %s stage.
Your job is to verify the CURRENT findings only. Do not invent new findings.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

<current_findings_summary>
%s
</current_findings_summary>

Rules:
1. Re-read the codebase using the provided tools and validate each current finding against actual code evidence.
2. Do NOT add new findings. Keep the same set of findings, but you may reject unsupported ones.
3. Preserve all existing fields for each finding.
4. Add or update these fields on every finding:
   - "verification_status": "confirmed", "uncertain", or "rejected"
   - "reviewed_severity": normalized severity after revalidation
   - "verification_reason": concise evidence-based explanation
5. Use "confirmed" only when the vulnerable path and impact are strongly supported by code.
6. Use "uncertain" when some evidence exists but exploitability, reachability, or impact is not fully established.
7. Use "rejected" for false positives, duplicates, schema mismatches, or claims not supported by code.
8. Only adjust severity through "reviewed_severity". Keep the original "severity" field unchanged.
9. Use query_routes for exact route subsets and query_stage_output for exact current findings while re-reading code.
10. Final output MUST be the full JSON array with the same findings and the new review fields. Return JSON only.`, summarysvc.StageLabel(stage), task.BasePath, BuildKnownRoutesContext(task, manifest), BuildCurrentFindingsContext(stage, existing)), nil
	default:
		return basePrompt, nil
	}
}

func currentJSONArrayForRun(task *model.Task, currentStage *model.TaskStage, stage string) ([]map[string]any, error) {
	if stage == "init" {
		results, ok := summarysvc.ParseJSONArray(task.OutputJSON, task.Result)
		if !ok {
			return nil, fmt.Errorf("current route inventory is not available as structured JSON")
		}
		return results, nil
	}
	if currentStage == nil {
		return nil, fmt.Errorf("stage %q is not loaded", stage)
	}
	results, ok := summarysvc.ParseJSONArray(currentStage.OutputJSON, currentStage.Result)
	if !ok {
		return nil, fmt.Errorf("stage %q does not have structured JSON output yet", stage)
	}
	return results, nil
}

func finalizeRunOutput(
	task *model.Task,
	stage string,
	currentStage *model.TaskStage,
	kind StageRunKind,
	content string,
) (json.RawMessage, model.TaskStageMeta, error) {
	if stage == "init" {
		if kind == StageRunRevalidate {
			return nil, model.TaskStageMeta{}, fmt.Errorf("route inventory does not support revalidation")
		}
		routes, err := parseJSONArrayOutputWithRepair(content, stage)
		if err != nil {
			return nil, model.TaskStageMeta{}, err
		}
		if kind == StageRunGapCheck {
			existing, err := currentJSONArrayForRun(task, currentStage, stage)
			if err != nil {
				return nil, model.TaskStageMeta{}, err
			}
			routes = mergeRouteInventory(existing, routes)
		}
		blob, err := marshalRaw(routes)
		return blob, model.TaskStageMeta{LastRunKind: string(kind)}, err
	}

	if currentStage == nil {
		return nil, model.TaskStageMeta{}, fmt.Errorf("stage %q is not loaded", stage)
	}
	existing, err := currentJSONArrayForRun(task, currentStage, stage)
	if err != nil && kind != StageRunInitial {
		return nil, model.TaskStageMeta{}, err
	}

	switch kind {
	case StageRunInitial:
		next, err := parseJSONArrayOutputWithRepair(content, stage)
		if err != nil {
			return nil, model.TaskStageMeta{}, err
		}
		next = normalizeInitialFindings(stage, next)
		blob, err := marshalRaw(next)
		return blob, model.TaskStageMeta{LastRunKind: string(kind)}, err
	case StageRunGapCheck:
		candidates, err := parseJSONArrayOutputWithRepair(content, stage)
		if err != nil {
			return nil, model.TaskStageMeta{}, err
		}
		final, meta := mergeGapCheckFindings(stage, existing, candidates)
		blob, err := marshalRaw(final)
		return blob, meta, err
	case StageRunRevalidate:
		reviewed, err := parseJSONArrayOutputWithRepair(content, stage)
		if err != nil {
			return nil, model.TaskStageMeta{}, err
		}
		final, meta := applyRevalidationFindings(stage, existing, reviewed)
		blob, err := marshalRaw(final)
		return blob, meta, err
	default:
		return nil, model.TaskStageMeta{}, fmt.Errorf("unsupported run kind %q", kind)
	}
}

func parseJSONArrayOutputWithRepair(content string, stage string) ([]map[string]any, error) {
	parse := func(raw string) ([]map[string]any, error) {
		jsonPart := extractJSON(raw)
		if !json.Valid([]byte(jsonPart)) {
			return nil, fmt.Errorf("AI output is not valid JSON")
		}
		items, ok := summarysvc.ParseJSONArray(json.RawMessage(jsonPart), "")
		if !ok {
			return nil, fmt.Errorf("AI output is not a JSON array")
		}
		return items, nil
	}

	items, err := parse(content)
	if err == nil {
		return items, nil
	}

	repaired, repairErr := RepairJSON(content, stage)
	if repairErr != nil {
		return nil, err
	}
	items, repairedErr := parse(repaired)
	if repairedErr != nil {
		return nil, repairedErr
	}
	return items, nil
}

func normalizeInitialFindings(stage string, findings []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(findings))
	for _, finding := range findings {
		out = append(out, normalizeFinding(stage, finding, "initial"))
	}
	return dedupeFindings(stage, out, nil)
}

func mergeGapCheckFindings(stage string, existing, candidates []map[string]any) ([]map[string]any, model.TaskStageMeta) {
	current := dedupeFindings(stage, tagExistingOrigins(stage, existing), nil)
	known := make(map[string]map[string]any, len(current))
	for _, finding := range current {
		known[findingSignature(stage, finding)] = finding
	}

	for _, candidate := range candidates {
		normalized := normalizeFinding(stage, candidate, "gap_check")
		signature := findingSignature(stage, normalized)
		if existingFinding, ok := known[signature]; ok {
			known[signature] = mergeFindingMaps(existingFinding, normalized)
			continue
		}
		known[signature] = normalized
		current = append(current, normalized)
	}

	final := dedupeFindings(stage, current, nil)
	meta := buildReviewMeta(final, StageRunGapCheck)
	if added := len(final) - len(dedupeFindings(stage, tagExistingOrigins(stage, existing), nil)); added > 0 {
		meta.ReviewSummary = fmt.Sprintf("Gap check merged %d additional finding(s) into the current stage result.", added)
	} else {
		meta.ReviewSummary = "Gap check completed without adding new findings."
	}
	now := time.Now()
	meta.GapCheckedAt = &now
	return final, meta
}

func applyRevalidationFindings(stage string, existing, reviewed []map[string]any) ([]map[string]any, model.TaskStageMeta) {
	current := dedupeFindings(stage, tagExistingOrigins(stage, existing), nil)
	reviewedBySignature := make(map[string]map[string]any, len(reviewed))
	for _, finding := range reviewed {
		normalized := normalizeFinding(stage, finding, "initial")
		reviewedBySignature[findingSignature(stage, normalized)] = normalized
	}

	final := make([]map[string]any, 0, len(current))
	for _, finding := range current {
		signature := findingSignature(stage, finding)
		if reviewedFinding, ok := reviewedBySignature[signature]; ok {
			final = append(final, mergeFindingMaps(finding, reviewedFinding))
			continue
		}
		copyFinding := cloneFinding(finding)
		if strings.TrimSpace(summarysvc.ExtractString(copyFinding["verification_status"])) == "" {
			copyFinding["verification_status"] = summarysvc.VerificationStatusUnreviewed
		}
		final = append(final, copyFinding)
	}

	final = dedupeFindings(stage, final, nil)
	meta := buildReviewMeta(final, StageRunRevalidate)
	now := time.Now()
	meta.RevalidatedAt = &now
	meta.ReviewSummary = fmt.Sprintf(
		"Revalidation completed: %d confirmed, %d uncertain, %d rejected.",
		meta.ConfirmedCount,
		meta.UncertainCount,
		meta.RejectedCount,
	)
	return final, meta
}

func buildReviewMeta(findings []map[string]any, kind StageRunKind) model.TaskStageMeta {
	meta := model.TaskStageMeta{LastRunKind: string(kind)}
	for _, finding := range findings {
		switch summarysvc.FindingVerificationStatus(finding) {
		case summarysvc.VerificationStatusConfirmed:
			meta.ConfirmedCount++
		case summarysvc.VerificationStatusRejected:
			meta.RejectedCount++
		case summarysvc.VerificationStatusUncertain:
			meta.UncertainCount++
		}
	}
	return meta
}

func tagExistingOrigins(stage string, findings []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(findings))
	for _, finding := range findings {
		origin := strings.TrimSpace(summarysvc.ExtractString(finding["origin"]))
		if origin == "" {
			origin = "initial"
		}
		out = append(out, normalizeFinding(stage, finding, origin))
	}
	return out
}

func normalizeFinding(stage string, finding map[string]any, defaultOrigin string) map[string]any {
	out := cloneFinding(finding)
	if stage != "init" && stageFindingType(stage) != "" && strings.TrimSpace(summarysvc.ExtractString(out["type"])) == "" {
		out["type"] = stageFindingType(stage)
	}
	if severity := strings.TrimSpace(summarysvc.ExtractString(out["severity"])); severity != "" {
		out["severity"] = summarysvc.NormalizeSeverity(severity)
	}
	if reviewed := strings.TrimSpace(summarysvc.ExtractString(out["reviewed_severity"])); reviewed != "" {
		out["reviewed_severity"] = summarysvc.NormalizeSeverity(reviewed)
	}
	if stage != "init" {
		origin := strings.TrimSpace(summarysvc.ExtractString(out["origin"]))
		if origin == "" {
			origin = defaultOrigin
		}
		out["origin"] = origin
		status := summarysvc.FindingVerificationStatus(out)
		out["verification_status"] = status
		if reason := strings.TrimSpace(summarysvc.ExtractString(out["verification_reason"])); reason == "" && status == summarysvc.VerificationStatusUnreviewed {
			delete(out, "verification_reason")
		}
	}
	return out
}

func dedupeFindings(stage string, findings []map[string]any, seeded map[string]map[string]any) []map[string]any {
	index := seeded
	if index == nil {
		index = make(map[string]map[string]any, len(findings))
	}
	order := make([]string, 0, len(findings))
	for _, finding := range findings {
		signature := findingSignature(stage, finding)
		if existing, ok := index[signature]; ok {
			index[signature] = mergeFindingMaps(existing, finding)
			continue
		}
		index[signature] = cloneFinding(finding)
		order = append(order, signature)
	}

	out := make([]map[string]any, 0, len(order))
	for _, signature := range order {
		out = append(out, index[signature])
	}
	return out
}

func mergeRouteInventory(existing, next []map[string]any) []map[string]any {
	combined := append(cloneFindingSlice(existing), cloneFindingSlice(next)...)
	index := make(map[string]map[string]any, len(combined))
	order := make([]string, 0, len(combined))
	for _, route := range combined {
		signature := routeSignature(route)
		if existingRoute, ok := index[signature]; ok {
			index[signature] = mergeFindingMaps(existingRoute, route)
			continue
		}
		index[signature] = cloneFinding(route)
		order = append(order, signature)
	}
	out := make([]map[string]any, 0, len(order))
	for _, signature := range order {
		out = append(out, index[signature])
	}
	return out
}

func findingSignature(stage string, finding map[string]any) string {
	location, _ := finding["location"].(map[string]any)
	trigger, _ := finding["trigger"].(map[string]any)
	parts := []string{
		strings.ToLower(strings.TrimSpace(summarysvc.ExtractString(finding["type"]))),
		strings.ToLower(strings.TrimSpace(summarysvc.ExtractString(finding["subtype"]))),
		strings.ToLower(strings.TrimSpace(summarysvc.ExtractString(location["file"]))),
		strings.TrimSpace(summarysvc.ExtractString(location["line"])),
		strings.ToLower(strings.TrimSpace(summarysvc.ExtractString(location["function"]))),
		strings.ToUpper(strings.TrimSpace(summarysvc.ExtractString(trigger["method"]))),
		strings.TrimSpace(summarysvc.ExtractString(trigger["path"])),
		strings.TrimSpace(summarysvc.ExtractString(trigger["parameter"])),
	}
	signature := strings.Join(parts, "|")
	if strings.Trim(signature, "|") != "" {
		return signature
	}
	return strings.ToLower(strings.TrimSpace(summarysvc.ExtractString(finding["description"])))
}

func routeSignature(route map[string]any) string {
	return strings.Join([]string{
		strings.ToUpper(strings.TrimSpace(summarysvc.ExtractString(route["method"]))),
		strings.TrimSpace(summarysvc.ExtractString(route["path"])),
		strings.TrimSpace(summarysvc.ExtractString(route["source"])),
	}, "|")
}

func mergeFindingMaps(base, overlay map[string]any) map[string]any {
	merged := cloneFinding(base)
	keys := make([]string, 0, len(overlay))
	for key := range overlay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := overlay[key]
		if isMeaningfulValue(value) {
			merged[key] = value
		}
	}
	return merged
}

func cloneFindingSlice(input []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(input))
	for _, item := range input {
		out = append(out, cloneFinding(item))
	}
	return out
}

func cloneFinding(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = cloneFinding(typed)
		case []any:
			cloned := make([]any, 0, len(typed))
			for _, item := range typed {
				if nested, ok := item.(map[string]any); ok {
					cloned = append(cloned, cloneFinding(nested))
					continue
				}
				cloned = append(cloned, item)
			}
			out[key] = cloned
		default:
			out[key] = value
		}
	}
	return out
}

func isMeaningfulValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func marshalIndented(value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func marshalRaw(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}
