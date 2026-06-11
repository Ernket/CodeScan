package scanner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codescan/internal/model"
	summarysvc "codescan/internal/service/summary"
)

const (
	runtimeSubmittedFindingsFile    = "submitted_findings.json"
	runtimeSubmittedReviewsFile     = "submitted_reviews.json"
	maxSubmittedFindingsPerToolCall = 5
	maxSubmittedReviewsPerToolCall  = 20
)

func (s *scanSession) submittedFindingsPath() string {
	return filepath.Join(s.runtimePath, runtimeSubmittedFindingsFile)
}

func (s *scanSession) submittedReviewsPath() string {
	return filepath.Join(s.runtimePath, runtimeSubmittedReviewsFile)
}

func (s *scanSession) appendSubmittedFindings(rawFindings []any) (submitted int, total int, err error) {
	findings, err := normalizeSubmittedFindings(s.stage, rawFindings)
	if err != nil {
		return 0, 0, err
	}

	existing, err := loadSubmittedFindingsFromPath(s.stage, s.submittedFindingsPath())
	if err != nil {
		return 0, 0, err
	}
	before := len(existing)
	merged := dedupeFindings(s.stage, append(existing, findings...), nil)
	if err := saveSubmittedItemsToPath(s.submittedFindingsPath(), merged); err != nil {
		return 0, 0, err
	}
	return len(merged) - before, len(merged), nil
}

func (s *scanSession) appendSubmittedReviews(rawReviews []any, existingFindings []map[string]any) (submitted int, total int, err error) {
	reviews, err := normalizeSubmittedReviews(rawReviews, existingFindings)
	if err != nil {
		return 0, 0, err
	}

	existing, err := loadSubmittedReviewsFromPath(s.submittedReviewsPath())
	if err != nil {
		return 0, 0, err
	}
	byIndex := map[int]map[string]any{}
	for _, review := range existing {
		if index, hasIndex, validIndex := revalidationFindingIndex(review); hasIndex && validIndex {
			byIndex[index] = review
		}
	}
	before := len(byIndex)
	for _, review := range reviews {
		index, _, _ := revalidationFindingIndex(review)
		byIndex[index] = review
	}

	merged := make([]map[string]any, 0, len(byIndex))
	for index := 0; index < len(existingFindings); index++ {
		if review, ok := byIndex[index]; ok {
			merged = append(merged, review)
		}
	}
	if err := saveSubmittedItemsToPath(s.submittedReviewsPath(), merged); err != nil {
		return 0, 0, err
	}
	return len(byIndex) - before, len(merged), nil
}

func loadSubmittedFindingsForTask(task *model.Task, stage string) ([]map[string]any, error) {
	if task == nil || stage == "init" {
		return nil, nil
	}
	return loadSubmittedFindingsFromPath(stage, filepath.Join(task.StageRuntimePath(stage), runtimeSubmittedFindingsFile))
}

func loadSubmittedReviewsForTask(task *model.Task, stage string) ([]map[string]any, error) {
	if task == nil || stage == "init" {
		return nil, nil
	}
	return loadSubmittedReviewsFromPath(filepath.Join(task.StageRuntimePath(stage), runtimeSubmittedReviewsFile))
}

func loadSubmittedFindingsFromPath(stage, path string) ([]map[string]any, error) {
	items, err := loadSubmittedItemsFromPath(path)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	if err := ValidateRepairJSONItems(stage, items); err != nil {
		return nil, fmt.Errorf("validate submitted findings: %w", err)
	}
	return dedupeFindings(stage, items, nil), nil
}

func loadSubmittedReviewsFromPath(path string) ([]map[string]any, error) {
	return loadSubmittedItemsFromPath(path)
}

func loadSubmittedItemsFromPath(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load submitted items: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}

	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse submitted items: %w", err)
	}
	return items, nil
}

func saveSubmittedItemsToPath(path string, items []map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create submitted item directory: %w", err)
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal submitted items: %w", err)
	}
	return writeFileAtomic(path, data)
}

func normalizeSubmittedFindings(stage string, rawFindings []any) ([]map[string]any, error) {
	if stage == "init" {
		return nil, fmt.Errorf("submit_findings is not available during init route discovery")
	}
	if len(rawFindings) == 0 {
		return nil, fmt.Errorf("findings must contain at least one finding")
	}
	if len(rawFindings) > maxSubmittedFindingsPerToolCall {
		return nil, fmt.Errorf("findings must contain at most %d findings per submit_findings call", maxSubmittedFindingsPerToolCall)
	}

	findings := make([]map[string]any, 0, len(rawFindings))
	for i, raw := range rawFindings {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			return nil, fmt.Errorf("findings[%d] must be an object", i)
		}
		findings = append(findings, cloneFinding(item))
	}
	if err := ValidateRepairJSONItems(stage, findings); err != nil {
		return nil, err
	}
	return findings, nil
}

func normalizeSubmittedReviews(rawReviews []any, existingFindings []map[string]any) ([]map[string]any, error) {
	if len(existingFindings) == 0 {
		return nil, fmt.Errorf("submit_reviews requires existing findings to review")
	}
	if len(rawReviews) == 0 {
		return nil, fmt.Errorf("reviews must contain at least one review")
	}
	if len(rawReviews) > maxSubmittedReviewsPerToolCall {
		return nil, fmt.Errorf("reviews must contain at most %d reviews per submit_reviews call", maxSubmittedReviewsPerToolCall)
	}

	reviews := make([]map[string]any, 0, len(rawReviews))
	seen := map[int]struct{}{}
	for i, raw := range rawReviews {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			return nil, fmt.Errorf("reviews[%d] must be an object", i)
		}
		index, hasIndex, validIndex := revalidationFindingIndex(item)
		if !hasIndex || !validIndex || index < 0 || index >= len(existingFindings) {
			return nil, fmt.Errorf("reviews[%d] has invalid finding_index %q", i, findingIndexDisplay(item["finding_index"]))
		}
		if _, exists := seen[index]; exists {
			return nil, fmt.Errorf("reviews[%d] duplicates finding_index %d", i, index)
		}
		seen[index] = struct{}{}
		status, ok := strictVerificationStatus(item)
		if !ok {
			return nil, fmt.Errorf("reviews[%d] has invalid verification_status %q", i, strings.TrimSpace(summarysvc.ExtractString(item["verification_status"])))
		}
		review := normalizeRevalidationReviewFields(item, status)
		review["finding_index"] = index
		reviews = append(reviews, review)
	}
	return reviews, nil
}
