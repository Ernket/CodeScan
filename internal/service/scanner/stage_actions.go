package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"codescan/internal/config"
	"codescan/internal/model"
	summarysvc "codescan/internal/service/summary"

	"github.com/sashabaranov/go-openai"
)

type StageRunKind string

const (
	StageRunInitial    StageRunKind = "initial"
	StageRunGapCheck   StageRunKind = "gap_check"
	StageRunRevalidate StageRunKind = "revalidate"
)

const (
	defaultRevalidationRepairReason = "\u590d\u6838\u8f93\u51fa\u672a\u7ed9\u51fa\u660e\u786e\u7ed3\u8bba\uff0c\u5df2\u6309\u4e0d\u786e\u5b9a\u5904\u7406\u3002"
	routeDiscoveryCompleteMarker    = "ROUTE_DISCOVERY_COMPLETE"
	findingDiscoveryCompleteMarker  = "FINDING_DISCOVERY_COMPLETE"
	revalidationCompleteMarker      = "REVALIDATION_COMPLETE"
)

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

func appendSubmissionWorkflowGuidance(prompt string, stage string, kind StageRunKind) string {
	kind = normalizeStageRunKind(string(kind))
	if stage == "init" {
		return strings.TrimSpace(prompt)
	}
	if kind == StageRunRevalidate {
		return strings.TrimSpace(prompt) + fmt.Sprintf(`

STABLE REVIEW SUBMISSION WORKFLOW:
- Use submit_reviews to submit revalidation conclusions incrementally in batches of 1-%d short review objects.
- Each review object MUST be: {"finding_index":0,"verification_status":"confirmed","reviewed_severity":"HIGH","verification_reason":"..."}.
- Submit exactly one review for every current finding_index from query_stage_output. Do not submit new vulnerability details.
- Do NOT output the full reviewed finding array in the final response. The backend persists submitted reviews and merges only verification_status, reviewed_severity, and verification_reason onto the original findings.
- When every current finding has been submitted, output exactly: %s
- If there are no current findings to review, output exactly: %s`, maxSubmittedReviewsPerToolCall, revalidationCompleteMarker, revalidationCompleteMarker)
	}

	scopeInstruction := "Submit every confirmed vulnerability finding for this stage."
	if kind == StageRunGapCheck {
		scopeInstruction = "Submit only newly confirmed missing findings for this gap-check pass; the backend will merge them with the current stage result."
	}
	return strings.TrimSpace(prompt) + fmt.Sprintf(`

STABLE FINDING SUBMISSION WORKFLOW:
- Use submit_findings to submit confirmed findings incrementally in batches of 1-%d complete finding objects.
- Each submitted finding MUST match the current stage schema and include the same evidence fields required by the final JSON format.
- %s
- Submit findings as soon as they are confirmed. Do not wait until the end to return one large JSON array.
- Do NOT output the full findings JSON array in the final response. The backend persists submitted findings and assembles the final OutputJSON.
- When the stage scope is exhausted, output exactly: %s
- If no vulnerabilities are found and no findings were submitted, output exactly: %s`, maxSubmittedFindingsPerToolCall, scopeInstruction, findingDiscoveryCompleteMarker, findingDiscoveryCompleteMarker)
}

func isSubmissionCompletionMarker(content string, marker string) bool {
	return strings.TrimSpace(content) == marker
}

func appendSubmittedProgressToError(task *model.Task, stage string, kind StageRunKind, err error) error {
	if task == nil || err == nil {
		return err
	}
	if stage == "init" {
		if submitted, loadErr := loadSubmittedRoutesForTask(task, stage); loadErr == nil && len(submitted) > 0 {
			return fmt.Errorf("%w; %d submitted route(s) were preserved in runtime state", err, len(submitted))
		}
		return err
	}
	if normalizeStageRunKind(string(kind)) == StageRunRevalidate {
		if submitted, loadErr := loadSubmittedReviewsForTask(task, stage); loadErr == nil && len(submitted) > 0 {
			return fmt.Errorf("%w; %d submitted review(s) were preserved in runtime state", err, len(submitted))
		}
		return err
	}
	if submitted, loadErr := loadSubmittedFindingsForTask(task, stage); loadErr == nil && len(submitted) > 0 {
		return fmt.Errorf("%w; %d submitted finding(s) were preserved in runtime state", err, len(submitted))
	}
	return err
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
- Prefer submit_routes for newly confirmed missing routes. If you use final JSON fallback instead, it MUST be the COMPLETE merged JSON array for this stage, not just the delta.

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
- Prefer submit_findings for newly confirmed missing findings. If you use final JSON fallback instead, it MUST be the COMPLETE merged JSON array for this stage, not just the delta.

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
2. You may think and use tools as needed. Prefer submit_reviews when available; legacy final JSON fallback must be the short JSON review array only.
3. Do NOT output only a prose conclusion. Natural-language summaries outside the JSON array are not an acceptable final answer.
4. Do NOT add new findings. Review exactly the current findings identified by their zero-based "finding_index".
5. Use query_stage_output to inspect exact current findings; its items include the stable "finding_index" you must return.
6. If you use legacy final JSON fallback instead of submit_reviews, final output MUST be a JSON array of short review objects only, in this shape:
   [{"finding_index":0,"verification_status":"confirmed","reviewed_severity":"HIGH","verification_reason":"..."}]
7. Each review object MUST contain:
   - "finding_index": the zero-based current finding index
   - "verification_status": "confirmed", "uncertain", or "rejected"
   - "reviewed_severity": normalized severity after revalidation
   - "verification_reason": concise evidence-based explanation
8. Do NOT include or rewrite original vulnerability detail fields such as type, subtype, severity, location, trigger, description, execution_logic, impact, poc_http, affected_endpoints, vulnerable_code, trigger_steps, or origin.
9. Use "confirmed" only when the vulnerable path and impact are strongly supported by code.
10. Use "uncertain" when some evidence exists but exploitability, reachability, or impact is not fully established.
11. Use "rejected" for false positives, duplicates, schema mismatches, or claims not supported by code.
12. Only adjust severity through "reviewed_severity". The backend will keep the original "severity" unchanged.
13. Use query_routes for exact route subsets and query_stage_output for exact current findings while re-reading code.
14. For legacy final JSON fallback, the final JSON array length MUST equal the number of current findings being reviewed.
15. Empty strings, missing fields, translated values, and values like "verified" are invalid for "verification_status".
16. Return JSON only when using legacy fallback; otherwise follow the submit_reviews completion marker instructions appended later.
`, summarysvc.StageLabel(stage), task.BasePath, BuildKnownRoutesContext(task, manifest), BuildCurrentFindingsContext(stage, existing)), nil
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
		submittedRoutes, err := loadSubmittedRoutesForTask(task, stage)
		if err != nil {
			return nil, model.TaskStageMeta{}, err
		}
		routes := submittedRoutes
		if len(routes) == 0 {
			if isSubmissionCompletionMarker(content, routeDiscoveryCompleteMarker) {
				routes = []map[string]any{}
			} else {
				routes, err = parseJSONArrayOutputWithRepair(content, stage)
				if err != nil {
					return nil, model.TaskStageMeta{}, err
				}
			}
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
		submitted, err := loadSubmittedFindingsForTask(task, stage)
		if err != nil {
			return nil, model.TaskStageMeta{}, err
		}
		next := submitted
		if len(next) == 0 {
			if isSubmissionCompletionMarker(content, findingDiscoveryCompleteMarker) {
				next = []map[string]any{}
			} else {
				next, err = parseJSONArrayOutputWithRepair(content, stage)
				if err != nil {
					return nil, model.TaskStageMeta{}, err
				}
			}
		}
		next = normalizeInitialFindings(stage, next)
		blob, err := marshalRaw(next)
		return blob, model.TaskStageMeta{LastRunKind: string(kind)}, err
	case StageRunGapCheck:
		submitted, err := loadSubmittedFindingsForTask(task, stage)
		if err != nil {
			return nil, model.TaskStageMeta{}, err
		}
		candidates := submitted
		if len(candidates) == 0 {
			if isSubmissionCompletionMarker(content, findingDiscoveryCompleteMarker) {
				candidates = []map[string]any{}
			} else {
				candidates, err = parseJSONArrayOutputWithRepair(content, stage)
				if err != nil {
					return nil, model.TaskStageMeta{}, err
				}
			}
		}
		final, meta := mergeGapCheckFindings(stage, existing, candidates)
		blob, err := marshalRaw(final)
		return blob, meta, err
	case StageRunRevalidate:
		submitted, err := loadSubmittedReviewsForTask(task, stage)
		if err != nil {
			return nil, model.TaskStageMeta{}, err
		}
		reviewed := submitted
		if len(reviewed) == 0 {
			if isSubmissionCompletionMarker(content, revalidationCompleteMarker) {
				reviewed = []map[string]any{}
			} else {
				reviewed, err = parseRevalidationReviewOutput(content, stage)
				if err != nil {
					final, meta, repairErr := repairAndApplyRevalidationOutput(stage, existing, nil, content, err)
					if repairErr != nil {
						return nil, model.TaskStageMeta{}, repairErr
					}
					blob, err := marshalRaw(final)
					return blob, meta, err
				}
			}
		}
		final, meta, err := applyRevalidationFindings(stage, existing, reviewed)
		if err != nil {
			final, meta, repairErr := repairAndApplyRevalidationOutput(stage, existing, reviewed, content, err)
			if repairErr != nil {
				return nil, model.TaskStageMeta{}, repairErr
			}
			blob, err := marshalRaw(final)
			return blob, meta, err
		}
		blob, err := marshalRaw(final)
		return blob, meta, err
	default:
		return nil, model.TaskStageMeta{}, fmt.Errorf("unsupported run kind %q", kind)
	}
}

func repairAndApplyRevalidationOutput(stage string, existing, reviewed []map[string]any, rawContent string, validationErr error) ([]map[string]any, model.TaskStageMeta, error) {
	repaired, repairOutput, repairErr := repairRevalidationOutput(stage, existing, reviewed, rawContent, validationErr)
	if repairErr != nil {
		return nil, model.TaskStageMeta{}, newRevalidationProcessingError(validationErr, reviewed, repairOutput, repairErr)
	}

	final, meta, applyErr := applyRevalidationFindings(stage, existing, repaired)
	if applyErr != nil {
		return nil, model.TaskStageMeta{}, newRevalidationProcessingError(applyErr, reviewed, repairOutput, applyErr)
	}

	return final, meta, nil
}

type revalidationProcessingError struct {
	err             error
	parsedCandidate string
	repairOutput    string
	repairError     string
}

func newRevalidationProcessingError(err error, reviewed []map[string]any, repairOutput string, repairErr error) error {
	parsedCandidate := "(not decoded)"
	if reviewed != nil {
		if data, marshalErr := json.MarshalIndent(reviewed, "", "  "); marshalErr == nil {
			parsedCandidate = string(data)
		} else {
			parsedCandidate = fmt.Sprintf("(failed to marshal parsed candidate: %v)", marshalErr)
		}
	}

	repairError := ""
	if repairErr != nil {
		repairError = repairErr.Error()
	}

	return &revalidationProcessingError{
		err:             err,
		parsedCandidate: parsedCandidate,
		repairOutput:    repairOutput,
		repairError:     repairError,
	}
}

func (e *revalidationProcessingError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *revalidationProcessingError) ProcessingDiagnostics() string {
	if e == nil {
		return ""
	}
	repairOutput := strings.TrimSpace(e.repairOutput)
	if repairOutput == "" {
		repairOutput = "(empty)"
	}
	repairError := strings.TrimSpace(e.repairError)
	if repairError == "" {
		repairError = "(none)"
	}
	return strings.Join([]string{
		"Result processing diagnostics:",
		"parsed_candidate:",
		e.parsedCandidate,
		"repair_output:",
		repairOutput,
		"repair_error:",
		repairError,
	}, "\n")
}

func (e *revalidationProcessingError) ProcessingLogPreview() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"parsed_candidate_preview=%q repair_output_preview=%q repair_error_preview=%q",
		previewForLog(e.parsedCandidate, 160),
		previewForLog(e.repairOutput, 160),
		previewForLog(e.repairError, 160),
	)
}

func repairRevalidationOutput(stage string, existing, reviewed []map[string]any, rawContent string, validationErr error) ([]map[string]any, string, error) {
	existingJSON, err := marshalIndented(indexExistingFindings(stage, existing))
	if err != nil {
		return nil, "", err
	}
	reviewedJSON := "(not decoded)"
	if reviewed != nil {
		reviewedJSON, err = marshalIndented(reviewed)
		if err != nil {
			return nil, "", err
		}
	}
	if strings.TrimSpace(rawContent) == "" {
		rawContent = "(empty)"
	}

	prompt := fmt.Sprintf(`You are repairing a failed vulnerability revalidation JSON result for the %s stage.
The previous revalidation output failed validation with this error:
%s

Return ONLY a complete JSON array. Do not include markdown or explanations.

Rules:
1. The output array length MUST be exactly %d.
2. Return short review objects only: {"finding_index":0,"verification_status":"confirmed","reviewed_severity":"HIGH","verification_reason":"..."}.
3. Do NOT add, remove, split, or merge findings.
4. Use the existing findings as the source of truth for zero-based "finding_index" identity and ordering.
5. Every item MUST contain "verification_status" with exactly one of: "confirmed", "uncertain", "rejected".
6. Empty strings, missing values, translated values, and values like "verified" are invalid.
7. Every item MUST contain "reviewed_severity"; if the attempted output does not provide a clear value, copy the original "severity".
8. Every item MUST contain "verification_reason"; if the attempted output does not provide a clear evidence-based reason, use "%s"
9. If an attempted item has no valid verification_status, set "verification_status" to "uncertain".
10. Do NOT output original vulnerability detail fields such as type, subtype, severity, location, trigger, affected_endpoints, description, execution_logic, impact, poc_http, vulnerable_code, trigger_steps, or origin.

<existing_findings_with_indexes_json>
%s
</existing_findings_with_indexes_json>

<attempted_reviewed_json>
%s
</attempted_reviewed_json>

<raw_attempted_output>
%s
</raw_attempted_output>
`, summarysvc.StageLabel(stage), validationErr, len(existing), defaultRevalidationRepairReason, existingJSON, reviewedJSON, rawContent)

	client := aiClientFactory()
	resp, err := createChatCompletionWithRetry(context.Background(), client, prepareChatCompletionRequest(openai.ChatCompletionRequest{
		Model: config.AI.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
	}, chatCompletionPurposeAuxiliary), chatCompletionRetryHooks{})
	if err != nil {
		return nil, "", err
	}
	if len(resp.Choices) == 0 {
		return nil, "", fmt.Errorf("AI repair response did not include any choices")
	}

	repairOutput := resp.Choices[0].Message.Content
	items, err := parseRevalidationRepairOutput(repairOutput, stage)
	if err != nil {
		return nil, repairOutput, err
	}
	return items, repairOutput, nil
}

func parseRevalidationRepairOutput(content string, stage string) ([]map[string]any, error) {
	return parseRevalidationReviewOutput(content, stage)
}

func parseRevalidationReviewOutput(content string, stage string) ([]map[string]any, error) {
	stage, ok := NormalizeRepairStage(stage)
	if !ok {
		return nil, fmt.Errorf("unsupported repair stage: %s", stage)
	}
	if stage == "init" {
		return nil, fmt.Errorf("route inventory does not support revalidation")
	}

	jsonPart := strings.TrimSpace(extractJSON(content))
	if !json.Valid([]byte(jsonPart)) {
		return nil, fmt.Errorf("stage %q output is not valid JSON", stage)
	}
	if !strings.HasPrefix(jsonPart, "[") {
		return nil, fmt.Errorf("stage %q output must be a JSON array", stage)
	}

	decoder := json.NewDecoder(strings.NewReader(jsonPart))
	decoder.UseNumber()
	var items []map[string]any
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("stage %q output must be a JSON array: %w", stage, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("stage %q output contains invalid trailing JSON data: %w", stage, err)
		}
		return nil, fmt.Errorf("stage %q output contains trailing JSON data", stage)
	}
	if items == nil {
		items = []map[string]any{}
	}
	return items, nil
}

func parseJSONArrayOutputWithRepair(content string, stage string) ([]map[string]any, error) {
	parse := func(raw string) ([]map[string]any, error) {
		items, _, err := ParseValidatedRepairJSON(raw, stage)
		if err != nil {
			return nil, err
		}
		return items, nil
	}

	items, err := parse(content)
	if err == nil {
		return items, nil
	}

	repaired, repairErr := RepairJSON(content, stage)
	if repairErr != nil {
		return nil, repairErr
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

func applyRevalidationFindings(stage string, existing, reviewed []map[string]any) ([]map[string]any, model.TaskStageMeta, error) {
	current := tagExistingOrigins(stage, existing)
	if len(current) == 0 {
		if len(reviewed) != 0 {
			return nil, model.TaskStageMeta{}, fmt.Errorf(
				"Revalidation incomplete: 0 existing findings, %d reviewed, 0 unreviewed.",
				len(reviewed),
			)
		}
		meta := buildReviewMeta(nil, StageRunRevalidate)
		now := time.Now()
		meta.RevalidatedAt = &now
		meta.ReviewSummary = "Revalidation skipped: no findings to review."
		return []map[string]any{}, meta, nil
	}

	coverage, err := validateRevalidationCoverage(stage, current, reviewed)
	if err != nil {
		return nil, model.TaskStageMeta{}, err
	}

	final := make([]map[string]any, 0, len(current))
	for index, finding := range current {
		final = append(final, mergeRevalidationReviewFields(finding, coverage.reviewedByIndex[index]))
	}

	meta := buildReviewMeta(final, StageRunRevalidate)
	now := time.Now()
	meta.RevalidatedAt = &now
	meta.ReviewSummary = fmt.Sprintf(
		"Revalidation completed: %d reviewed, %d confirmed, %d uncertain, %d rejected.",
		coverage.reviewedCount,
		meta.ConfirmedCount,
		meta.UncertainCount,
		meta.RejectedCount,
	)
	return final, meta, nil
}

type revalidationCoverage struct {
	reviewedCount   int
	reviewedByIndex map[int]map[string]any
}

func validateRevalidationCoverage(stage string, existing, reviewed []map[string]any) (revalidationCoverage, error) {
	expectedBySignature := make(map[string][]int, len(existing))
	for index, finding := range existing {
		expectedBySignature[findingSignature(stage, finding)] = append(expectedBySignature[findingSignature(stage, finding)], index)
	}

	reviewedByIndex := make(map[int]map[string]any, len(reviewed))
	for i, finding := range reviewed {
		status, ok := strictVerificationStatus(finding)
		if !ok {
			return revalidationCoverage{}, fmt.Errorf(
				"Revalidation incomplete: %d existing findings, %d reviewed, %d unreviewed; item %d has invalid verification_status %q.",
				len(existing),
				len(reviewedByIndex),
				len(existing)-len(reviewedByIndex),
				i,
				strings.TrimSpace(summarysvc.ExtractString(finding["verification_status"])),
			)
		}

		review := normalizeRevalidationReviewFields(finding, status)
		if index, hasIndex, validIndex := revalidationFindingIndex(finding); hasIndex {
			if !validIndex || index < 0 || index >= len(existing) {
				return revalidationCoverage{}, fmt.Errorf(
					"Revalidation incomplete: %d existing findings, %d reviewed, %d unreviewed; item %d has invalid finding_index %q.",
					len(existing),
					len(reviewedByIndex),
					len(existing)-len(reviewedByIndex),
					i,
					findingIndexDisplay(finding["finding_index"]),
				)
			}
			if _, exists := reviewedByIndex[index]; exists {
				return revalidationCoverage{}, fmt.Errorf(
					"Revalidation incomplete: %d existing findings, %d reviewed, %d unreviewed; item %d duplicates an existing finding review.",
					len(existing),
					len(reviewedByIndex),
					len(existing)-len(reviewedByIndex),
					i,
				)
			}
			reviewedByIndex[index] = review
			continue
		}

		normalized := normalizeFinding(stage, finding, "initial")
		normalized["verification_status"] = status
		signature := findingSignature(stage, normalized)
		indices := expectedBySignature[signature]
		if len(indices) == 0 {
			return revalidationCoverage{}, fmt.Errorf(
				"Revalidation incomplete: %d existing findings, %d reviewed, %d unreviewed; item %d does not match an existing finding.",
				len(existing),
				len(reviewedByIndex),
				len(existing)-len(reviewedByIndex),
				i,
			)
		}
		assignedIndex := -1
		for _, index := range indices {
			if _, exists := reviewedByIndex[index]; !exists {
				assignedIndex = index
				break
			}
		}
		if assignedIndex == -1 {
			return revalidationCoverage{}, fmt.Errorf(
				"Revalidation incomplete: %d existing findings, %d reviewed, %d unreviewed; item %d duplicates an existing finding review.",
				len(existing),
				len(reviewedByIndex),
				len(existing)-len(reviewedByIndex),
				i,
			)
		}
		reviewedByIndex[assignedIndex] = review
	}

	reviewedCount := len(reviewedByIndex)
	if reviewedCount != len(existing) {
		return revalidationCoverage{}, fmt.Errorf(
			"Revalidation incomplete: %d existing findings, %d reviewed, %d unreviewed.",
			len(existing),
			reviewedCount,
			len(existing)-reviewedCount,
		)
	}

	return revalidationCoverage{reviewedCount: reviewedCount, reviewedByIndex: reviewedByIndex}, nil
}

func strictVerificationStatus(finding map[string]any) (string, bool) {
	status := strings.ToLower(strings.TrimSpace(summarysvc.ExtractString(finding["verification_status"])))
	switch status {
	case summarysvc.VerificationStatusConfirmed, summarysvc.VerificationStatusUncertain, summarysvc.VerificationStatusRejected:
		return status, true
	default:
		return "", false
	}
}

func normalizeRevalidationReviewFields(finding map[string]any, status string) map[string]any {
	review := map[string]any{
		"verification_status": status,
	}
	if reviewed := strings.TrimSpace(summarysvc.ExtractString(finding["reviewed_severity"])); reviewed != "" {
		review["reviewed_severity"] = summarysvc.NormalizeSeverity(reviewed)
	}
	if reason := strings.TrimSpace(summarysvc.ExtractString(finding["verification_reason"])); reason != "" {
		review["verification_reason"] = reason
	}
	return review
}

func mergeRevalidationReviewFields(base, review map[string]any) map[string]any {
	merged := cloneFinding(base)
	status, ok := strictVerificationStatus(review)
	if !ok {
		status = summarysvc.VerificationStatusUncertain
	}
	merged["verification_status"] = status

	reviewedSeverity := strings.TrimSpace(summarysvc.ExtractString(review["reviewed_severity"]))
	if reviewedSeverity == "" {
		reviewedSeverity = strings.TrimSpace(summarysvc.ExtractString(base["severity"]))
	}
	if reviewedSeverity != "" {
		merged["reviewed_severity"] = summarysvc.NormalizeSeverity(reviewedSeverity)
	}

	reason := strings.TrimSpace(summarysvc.ExtractString(review["verification_reason"]))
	if reason == "" {
		reason = defaultRevalidationRepairReason
	}
	merged["verification_reason"] = reason
	return merged
}

func revalidationFindingIndex(finding map[string]any) (int, bool, bool) {
	value, exists := finding["finding_index"]
	if !exists {
		return 0, false, false
	}
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 0)
		if err != nil {
			return 0, true, false
		}
		return int(parsed), true, true
	case float64:
		if typed != float64(int(typed)) {
			return 0, true, false
		}
		return int(typed), true, true
	case int:
		return typed, true, true
	case int64:
		return int(typed), true, int64(int(typed)) == typed
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, true, false
		}
		return parsed, true, true
	default:
		return 0, true, false
	}
}

func findingIndexDisplay(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(summarysvc.ExtractString(value))
}

func indexExistingFindings(stage string, findings []map[string]any) []map[string]any {
	indexed := tagExistingOrigins(stage, findings)
	for index, finding := range indexed {
		finding["finding_index"] = index
	}
	return indexed
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
