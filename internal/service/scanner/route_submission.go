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
	runtimeSubmittedRoutesFile    = "submitted_routes.json"
	maxSubmittedRoutesPerToolCall = 20
	maxRouteDescriptionRunes      = 160
)

func (s *scanSession) submittedRoutesPath() string {
	return filepath.Join(s.runtimePath, runtimeSubmittedRoutesFile)
}

func (s *scanSession) loadSubmittedRoutes() ([]map[string]any, error) {
	return loadSubmittedRoutesFromPath(s.submittedRoutesPath())
}

func (s *scanSession) saveSubmittedRoutes(routes []map[string]any) error {
	return saveSubmittedRoutesToPath(s.submittedRoutesPath(), routes)
}

func (s *scanSession) appendSubmittedRoutes(rawRoutes []any) (submitted int, total int, err error) {
	routes, err := normalizeSubmittedRoutes(rawRoutes)
	if err != nil {
		return 0, 0, err
	}

	existing, err := s.loadSubmittedRoutes()
	if err != nil {
		return 0, 0, err
	}
	before := len(dedupeSubmittedRoutes(existing))
	merged := mergeRouteInventory(existing, routes)
	if err := s.saveSubmittedRoutes(merged); err != nil {
		return 0, 0, err
	}
	return len(merged) - before, len(merged), nil
}

func loadSubmittedRoutesForTask(task *model.Task, stage string) ([]map[string]any, error) {
	if task == nil {
		return nil, nil
	}
	return loadSubmittedRoutesFromPath(filepath.Join(task.StageRuntimePath(stage), runtimeSubmittedRoutesFile))
}

func loadSubmittedRoutesFromPath(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("load submitted routes: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}

	var routes []map[string]any
	if err := json.Unmarshal(data, &routes); err != nil {
		return nil, fmt.Errorf("parse submitted routes: %w", err)
	}
	return dedupeSubmittedRoutes(routes), nil
}

func saveSubmittedRoutesToPath(path string, routes []map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create submitted route directory: %w", err)
	}
	data, err := json.MarshalIndent(dedupeSubmittedRoutes(routes), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal submitted routes: %w", err)
	}
	return writeFileAtomic(path, data)
}

func normalizeSubmittedRoutes(rawRoutes []any) ([]map[string]any, error) {
	if len(rawRoutes) == 0 {
		return nil, fmt.Errorf("routes must contain at least one route")
	}
	if len(rawRoutes) > maxSubmittedRoutesPerToolCall {
		return nil, fmt.Errorf("routes must contain at most %d routes per submit_routes call", maxSubmittedRoutesPerToolCall)
	}

	routes := make([]map[string]any, 0, len(rawRoutes))
	for i, raw := range rawRoutes {
		item, ok := raw.(map[string]any)
		if !ok || item == nil {
			return nil, fmt.Errorf("routes[%d] must be an object", i)
		}

		method := strings.ToUpper(strings.TrimSpace(summarysvc.ExtractString(item["method"])))
		path := strings.TrimSpace(summarysvc.ExtractString(item["path"]))
		source := filepath.ToSlash(strings.TrimSpace(summarysvc.ExtractString(item["source"])))
		description := strings.TrimSpace(summarysvc.ExtractString(item["description"]))
		if method == "" {
			return nil, fmt.Errorf("routes[%d].method is required", i)
		}
		if path == "" {
			return nil, fmt.Errorf("routes[%d].path is required", i)
		}
		if source == "" {
			return nil, fmt.Errorf("routes[%d].source is required", i)
		}
		if description == "" {
			description = fmt.Sprintf("%s %s", method, path)
		}
		description = trimRunes(description, maxRouteDescriptionRunes)

		routes = append(routes, map[string]any{
			"method":      method,
			"path":        path,
			"source":      source,
			"description": description,
		})
	}
	return routes, nil
}

func dedupeSubmittedRoutes(routes []map[string]any) []map[string]any {
	return mergeRouteInventory(nil, routes)
}

func trimRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
