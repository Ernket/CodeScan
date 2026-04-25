package scanner

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"codescan/internal/model"
)

func TestBuildProjectManifestIncludesLanguagesModulesRoutesAndHotspots(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepathJoin(root, "go.mod"), "module example.com/demo\n")
	mustWriteFile(t, filepathJoin(root, "cmd", "api", "routes.go"), `package api
import (
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)
func Register(r *gin.Engine) {
	r.GET("/health", health)
}
func Login(tokenString string) {
	_, _ = jwt.Parse(tokenString, func(token *jwt.Token) (any, error) { return []byte("secret"), nil })
}
`)
	mustWriteFile(t, filepathJoin(root, "frontend", "package.json"), `{"dependencies":{"vue":"^3.0.0"}}`)

	manifest, err := BuildProjectManifest(root)
	if err != nil {
		t.Fatalf("build project manifest: %v", err)
	}

	if !containsString(manifest.Languages, "go") || !containsString(manifest.Languages, "javascript") {
		t.Fatalf("expected go and javascript languages, got %+v", manifest.Languages)
	}
	if !containsString(manifest.FrameworkHints, "gin") || !containsString(manifest.FrameworkHints, "vue") {
		t.Fatalf("expected gin and vue framework hints, got %+v", manifest.FrameworkHints)
	}
	if !containsString(manifest.ModuleRoots, ".") || !containsString(manifest.ModuleRoots, "frontend") {
		t.Fatalf("expected module roots to include . and frontend, got %+v", manifest.ModuleRoots)
	}
	if !manifestHasRule(manifest.RouteCandidateFiles, "cmd/api/routes.go", "gin_echo_fiber") {
		t.Fatalf("expected route candidate hit for cmd/api/routes.go, got %+v", manifest.RouteCandidateFiles)
	}
	if !manifestHasRule(manifest.StageHotspots["auth"], "cmd/api/routes.go", "jwt_usage") {
		t.Fatalf("expected auth hotspot hit for cmd/api/routes.go, got %+v", manifest.StageHotspots["auth"])
	}
}

func TestStructuredPromptContextsUseSummariesInsteadOfFullJSON(t *testing.T) {
	task := &model.Task{
		OutputJSON: json.RawMessage(`[{"method":"GET","path":"/super-secret","source":"internal/api.go","description":"UNIQUE_ROUTE_MARKER"}]`),
	}

	routesSummary := BuildKnownRoutesContext(task, &ProjectManifest{ModuleRoots: []string{"."}})
	if strings.Contains(routesSummary, "UNIQUE_ROUTE_MARKER") || strings.Contains(routesSummary, "/super-secret") {
		t.Fatalf("expected route summary to avoid embedding full route JSON, got %q", routesSummary)
	}
	if !containsAll(routesSummary, "total_routes: 1", "query_routes", "module_distribution") {
		t.Fatalf("expected route summary guidance, got %q", routesSummary)
	}

	findingsSummary := BuildCurrentFindingsContext("auth", []map[string]any{
		{
			"description":         "UNIQUE_FINDING_MARKER",
			"origin":              "gap_check",
			"verification_status": "confirmed",
		},
	})
	if strings.Contains(findingsSummary, "UNIQUE_FINDING_MARKER") {
		t.Fatalf("expected findings summary to avoid embedding full finding JSON, got %q", findingsSummary)
	}
	if !containsAll(findingsSummary, "total_findings: 1", "query_stage_output", "confirmed: 1") {
		t.Fatalf("expected findings summary guidance, got %q", findingsSummary)
	}
}

func TestExecuteQueryManifestFiltersByStageAndRule(t *testing.T) {
	task := newTestTask(t)
	mustWriteFile(t, filepathJoin(task.BasePath, "go.mod"), "module example.com/demo\n")
	mustWriteFile(t, filepathJoin(task.BasePath, "internal", "auth.go"), `package internal
import "github.com/golang-jwt/jwt/v5"
func Validate(tokenString string) {
	_, _ = jwt.Parse(tokenString, func(token *jwt.Token) (any, error) { return []byte("secret"), nil })
}
`)

	output := ExecuteQueryManifest(task, "auth", "", "jwt", 0, 10)

	if !containsAll(output, `"stage": "auth"`, `"rule_name": "jwt_usage"`, `"path": "internal/auth.go"`) {
		t.Fatalf("expected filtered manifest output, got %q", output)
	}
	if strings.Contains(output, `"section": "route_candidate"`) {
		t.Fatalf("expected auth-stage query to exclude route candidates, got %q", output)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func manifestHasRule(files []ProjectManifestIndexedFile, path string, ruleName string) bool {
	for _, file := range files {
		if file.Path != path {
			continue
		}
		for _, rule := range file.Rules {
			if rule.Name == ruleName {
				return true
			}
		}
	}
	return false
}

func filepathJoin(parts ...string) string {
	return filepath.ToSlash(filepath.Join(parts...))
}
