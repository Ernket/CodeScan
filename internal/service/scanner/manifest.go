package scanner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"codescan/internal/database"
	"codescan/internal/model"
	summarysvc "codescan/internal/service/summary"
)

const (
	manifestScanMaxFileBytes   = 2 * 1024 * 1024
	manifestMaxLinesPerRule    = 12
	manifestMaxFilesPerSection = 200
	manifestDefaultQueryLimit  = 20
)

type ProjectManifest struct {
	GeneratedAt         time.Time                               `json:"generated_at"`
	Languages           []string                                `json:"languages"`
	FrameworkHints      []string                                `json:"framework_hints"`
	ModuleRoots         []string                                `json:"module_roots"`
	RouteCandidateFiles []ProjectManifestIndexedFile            `json:"route_candidate_files"`
	StageHotspots       map[string][]ProjectManifestIndexedFile `json:"stage_hotspots"`
}

type ProjectManifestIndexedFile struct {
	Module string                    `json:"module"`
	Path   string                    `json:"path"`
	Rules  []ProjectManifestRuleHits `json:"rules"`
}

type ProjectManifestRuleHits struct {
	Name  string `json:"name"`
	Lines []int  `json:"lines"`
}

type manifestQueryItem struct {
	Section  string `json:"section"`
	Stage    string `json:"stage,omitempty"`
	Module   string `json:"module"`
	Path     string `json:"path"`
	RuleName string `json:"rule_name"`
	Lines    []int  `json:"lines"`
}

type manifestRule struct {
	Name    string
	Pattern *regexp.Regexp
}

type manifestFileAccumulator struct {
	Path  string
	Rules map[string][]int
}

var routeCandidateRules = []manifestRule{
	{Name: "gin_echo_fiber", Pattern: regexp.MustCompile(`\.(GET|POST|PUT|DELETE|PATCH|Any|Group|Static)\(`)},
	{Name: "chi", Pattern: regexp.MustCompile(`\.(Get|Post|Put|Delete|Patch|Route|Mount)\(`)},
	{Name: "gorilla_mux", Pattern: regexp.MustCompile(`\.(HandleFunc|Handle|PathPrefix)\(`)},
	{Name: "stdlib_http", Pattern: regexp.MustCompile(`http\.(HandleFunc|Handle)\(`)},
	{Name: "beego", Pattern: regexp.MustCompile(`beego\.Router\(`)},
	{Name: "spring_mapping", Pattern: regexp.MustCompile(`@(GetMapping|PostMapping|PutMapping|DeleteMapping|RequestMapping|PatchMapping|Path)`)},
	{Name: "flask", Pattern: regexp.MustCompile(`@app\.route\(`)},
	{Name: "fastapi", Pattern: regexp.MustCompile(`@app\.(get|post|put|delete|patch)\(`)},
	{Name: "django", Pattern: regexp.MustCompile(`\b(path|url)\(`)},
	{Name: "express_koa", Pattern: regexp.MustCompile(`\.(get|post|put|delete|patch|use|route)\(`)},
	{Name: "nestjs", Pattern: regexp.MustCompile(`@(Get|Post|Put|Delete|Patch|Controller)\(`)},
}

var stageHotspotRules = map[string][]manifestRule{
	"rce": {
		{Name: "command_exec", Pattern: regexp.MustCompile(`\b(exec\.Command|system\(|popen\(|Runtime\.getRuntime\(\)\.exec|ProcessBuilder\()`)},
		{Name: "code_eval", Pattern: regexp.MustCompile(`\b(eval\(|Function\(|assert\(|vm\.runIn)`)},
		{Name: "unsafe_deserialize", Pattern: regexp.MustCompile(`\b(ObjectInputStream|gob\.NewDecoder|pickle\.load|yaml\.load)`)},
		{Name: "template_exec", Pattern: regexp.MustCompile(`\b(template\.Must|ExecuteTemplate|render_template_string)`)},
	},
	"injection": {
		{Name: "raw_sql", Pattern: regexp.MustCompile(`\b(SELECT|INSERT|UPDATE|DELETE)\b`)},
		{Name: "sql_format", Pattern: regexp.MustCompile(`\b(fmt\.Sprintf|Format\(|\+)\b`)},
		{Name: "orm_raw_query", Pattern: regexp.MustCompile(`\b(Raw\(|Exec\(|Query\(|query\()`)},
		{Name: "nosql_query", Pattern: regexp.MustCompile(`\b(find\(|aggregate\(|where\(|\$ne|\$gt|\$regex)`)},
	},
	"auth": {
		{Name: "auth_entry", Pattern: regexp.MustCompile(`\b(login|signin|signup|register|resetPassword|changePassword)\b`)},
		{Name: "session_state", Pattern: regexp.MustCompile(`\b(session|cookie|Set-Cookie|gorilla/sessions|express-session)\b`)},
		{Name: "jwt_usage", Pattern: regexp.MustCompile(`\b(jwt|jsonwebtoken|ParseWithClaims|SignedString)\b`)},
		{Name: "auth_middleware", Pattern: regexp.MustCompile(`\b(Auth|Authenticate|Authorization|RequireLogin|middleware)\b`)},
	},
	"access": {
		{Name: "permission_check", Pattern: regexp.MustCompile(`\b(canAccess|hasRole|hasPermission|authorize|IsAdmin|RequireRole)\b`)},
		{Name: "admin_surface", Pattern: regexp.MustCompile(`\b(admin|role|permission|tenant|scope)\b`)},
		{Name: "object_lookup", Pattern: regexp.MustCompile(`\b(FindByID|GetByID|LoadByID|SelectByID|owner_id|user_id)\b`)},
	},
	"xss": {
		{Name: "html_render", Pattern: regexp.MustCompile(`\b(template\.HTML|innerHTML|dangerouslySetInnerHTML|v-html)\b`)},
		{Name: "unescaped_output", Pattern: regexp.MustCompile(`\b(ExecuteTemplate|Mustache|render|Markup)\b`)},
		{Name: "sink_assignment", Pattern: regexp.MustCompile(`\b(document\.write|Response\.Write|WriteString)\b`)},
	},
	"config": {
		{Name: "debug_mode", Pattern: regexp.MustCompile(`\b(debug|DEBUG|devMode|development)\b`)},
		{Name: "secret_material", Pattern: regexp.MustCompile(`\b(secret|password|token|apikey|private_key)\b`)},
		{Name: "cors_tls", Pattern: regexp.MustCompile(`\b(CORS|AllowOrigin|InsecureSkipVerify|sslmode=disable)\b`)},
		{Name: "dependency_file", Pattern: regexp.MustCompile(`\b(package\.json|pom\.xml|go\.mod|requirements\.txt|package-lock\.json)\b`)},
	},
	"fileop": {
		{Name: "upload_entry", Pattern: regexp.MustCompile(`\b(FormFile|SaveUploadedFile|MultipartFile|upload)\b`)},
		{Name: "file_read", Pattern: regexp.MustCompile(`\b(ReadFile|OpenFile|ServeFile|SendFile|Download)\b`)},
		{Name: "path_join", Pattern: regexp.MustCompile(`\b(filepath\.Join|path\.Join|Clean\(|Abs\()\b`)},
		{Name: "archive_extract", Pattern: regexp.MustCompile(`\b(zip\.NewReader|tar\.NewReader|Extract|Unzip)\b`)},
	},
	"logic": {
		{Name: "amount_quantity", Pattern: regexp.MustCompile(`\b(amount|price|total|discount|quantity|balance|inventory)\b`)},
		{Name: "workflow_state", Pattern: regexp.MustCompile(`\b(status|state|approve|refund|checkout|redeem|settle)\b`)},
		{Name: "race_window", Pattern: regexp.MustCompile(`\b(transaction|lock|mutex|compareAndSwap|SELECT FOR UPDATE|retry)\b`)},
		{Name: "rule_bypass", Pattern: regexp.MustCompile(`\b(limit|quota|threshold|eligibility|coupon|once)\b`)},
	},
}

var frameworkHintRules = []struct {
	Hint    string
	Pattern *regexp.Regexp
}{
	{Hint: "gin", Pattern: regexp.MustCompile(`github\.com/gin-gonic/gin|\bgin\.`)},
	{Hint: "echo", Pattern: regexp.MustCompile(`github\.com/labstack/echo|\becho\.`)},
	{Hint: "fiber", Pattern: regexp.MustCompile(`github\.com/gofiber/fiber|\bfiber\.`)},
	{Hint: "chi", Pattern: regexp.MustCompile(`github\.com/go-chi/chi|\bchi\.`)},
	{Hint: "gorilla-mux", Pattern: regexp.MustCompile(`gorilla/mux`)},
	{Hint: "spring", Pattern: regexp.MustCompile(`spring-boot|@RestController|@RequestMapping`)},
	{Hint: "flask", Pattern: regexp.MustCompile(`flask|@app\.route`)},
	{Hint: "fastapi", Pattern: regexp.MustCompile(`fastapi|@app\.(get|post|put|delete|patch)`)},
	{Hint: "django", Pattern: regexp.MustCompile(`django|urlpatterns`)},
	{Hint: "express", Pattern: regexp.MustCompile(`express\(|require\(['"]express['"]\)|from ['"]express['"]`)},
	{Hint: "koa", Pattern: regexp.MustCompile(`require\(['"]koa['"]\)|from ['"]koa['"]`)},
	{Hint: "nestjs", Pattern: regexp.MustCompile(`@nestjs/|@Controller\(`)},
	{Hint: "vue", Pattern: regexp.MustCompile(`vue|vue-router`)},
	{Hint: "react", Pattern: regexp.MustCompile(`react|react-router`)},
}

var languageByExtension = map[string]string{
	".go":    "go",
	".js":    "javascript",
	".jsx":   "javascript",
	".ts":    "typescript",
	".tsx":   "typescript",
	".java":  "java",
	".py":    "python",
	".php":   "php",
	".rb":    "ruby",
	".cs":    "csharp",
	".kt":    "kotlin",
	".swift": "swift",
	".rs":    "rust",
}

var moduleRootFiles = map[string]struct{}{
	"go.mod":              {},
	"package.json":        {},
	"pom.xml":             {},
	"build.gradle":        {},
	"build.gradle.kts":    {},
	"settings.gradle":     {},
	"settings.gradle.kts": {},
	"requirements.txt":    {},
	"pyproject.toml":      {},
	"Pipfile":             {},
	"composer.json":       {},
	"Cargo.toml":          {},
}

func EnsureProjectManifest(task *model.Task) (*ProjectManifest, error) {
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}

	task.BasePath = task.GetBasePath()
	path := task.ProjectManifestPath()
	if data, err := os.ReadFile(path); err == nil {
		var manifest ProjectManifest
		if unmarshalErr := json.Unmarshal(data, &manifest); unmarshalErr == nil {
			return &manifest, nil
		}
	}

	manifest, err := BuildProjectManifest(task.BasePath)
	if err != nil {
		return nil, err
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return nil, err
	}
	return manifest, nil
}

func BuildProjectManifest(basePath string) (*ProjectManifest, error) {
	languageSet := map[string]struct{}{}
	frameworkSet := map[string]struct{}{}
	moduleRootSet := map[string]struct{}{".": {}}
	routeMatches := map[string]*manifestFileAccumulator{}
	stageMatches := map[string]map[string]*manifestFileAccumulator{}

	for stage := range stageHotspotRules {
		stageMatches[stage] = map[string]*manifestFileAccumulator{}
	}

	err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if shouldSkipDir(info) {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, relErr := filepath.Rel(basePath, path)
		if relErr != nil {
			return nil
		}
		relPath = filepath.ToSlash(filepath.Clean(relPath))

		name := info.Name()
		if language := detectLanguage(name, relPath); language != "" {
			languageSet[language] = struct{}{}
		}
		if _, ok := moduleRootFiles[name]; ok {
			moduleRootSet[manifestModuleDir(relPath)] = struct{}{}
		}

		if info.Size() <= 0 || info.Size() > manifestScanMaxFileBytes {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil || looksBinary(content) {
			return nil
		}

		text := string(content)
		for _, rule := range frameworkHintRules {
			if rule.Pattern.MatchString(text) || rule.Pattern.MatchString(relPath) {
				frameworkSet[rule.Hint] = struct{}{}
			}
		}

		scanner := bufio.NewScanner(strings.NewReader(text))
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			collectManifestLineHits(routeMatches, relPath, routeCandidateRules, line, lineNo)
			for stage, rules := range stageHotspotRules {
				collectManifestLineHits(stageMatches[stage], relPath, rules, line, lineNo)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	moduleRoots := sortedStringKeys(moduleRootSet)
	manifest := &ProjectManifest{
		GeneratedAt:         time.Now(),
		Languages:           sortedStringKeys(languageSet),
		FrameworkHints:      sortedStringKeys(frameworkSet),
		ModuleRoots:         moduleRoots,
		RouteCandidateFiles: finalizeManifestFiles(routeMatches, moduleRoots),
		StageHotspots:       map[string][]ProjectManifestIndexedFile{},
	}

	for _, stage := range orderedAuditStages() {
		manifest.StageHotspots[stage] = finalizeManifestFiles(stageMatches[stage], moduleRoots)
	}

	return manifest, nil
}

func ExecuteQueryManifest(task *model.Task, stage, module, ruleName string, offset, limit int) string {
	manifest, err := EnsureProjectManifest(task)
	if err != nil {
		return fmt.Sprintf("Error loading project manifest: %v", err)
	}

	if limit <= 0 {
		limit = manifestDefaultQueryLimit
	}
	if offset < 0 {
		offset = 0
	}

	items := []manifestQueryItem{}
	includeRouteCandidates := strings.TrimSpace(stage) == "" || strings.EqualFold(strings.TrimSpace(stage), "init")
	if includeRouteCandidates {
		items = append(items, flattenManifestFiles(manifest.RouteCandidateFiles, "route_candidate", "", module, ruleName)...)
	}

	stageFilter := strings.TrimSpace(strings.ToLower(stage))
	for stageKey, files := range manifest.StageHotspots {
		if stageFilter != "" && stageFilter != stageKey {
			continue
		}
		items = append(items, flattenManifestFiles(files, "stage_hotspot", stageKey, module, ruleName)...)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Stage != items[j].Stage {
			return items[i].Stage < items[j].Stage
		}
		if items[i].Module != items[j].Module {
			return items[i].Module < items[j].Module
		}
		if items[i].Path != items[j].Path {
			return items[i].Path < items[j].Path
		}
		return items[i].RuleName < items[j].RuleName
	})

	total := len(items)
	paged := paginateManifestItems(items, offset, limit)
	payload := map[string]any{
		"languages":       manifest.Languages,
		"framework_hints": manifest.FrameworkHints,
		"module_roots":    manifest.ModuleRoots,
		"total":           total,
		"offset":          offset,
		"limit":           limit,
		"items":           paged,
	}
	return marshalIndentedOrError(payload)
}

func ExecuteQueryRoutes(task *model.Task, source, pathFilter, method string, offset, limit int) string {
	if limit <= 0 {
		limit = manifestDefaultQueryLimit
	}
	if offset < 0 {
		offset = 0
	}

	routes, ok := summarysvc.ParseJSONArray(task.OutputJSON, task.Result)
	if !ok {
		return "Route inventory is not available as structured JSON yet."
	}

	method = strings.ToUpper(strings.TrimSpace(method))
	source = strings.ToLower(strings.TrimSpace(source))
	pathFilter = strings.ToLower(strings.TrimSpace(pathFilter))

	filtered := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		routeMethod := strings.ToUpper(summarysvc.ExtractString(route["method"]))
		routePath := summarysvc.ExtractString(route["path"])
		routeSource := firstNonEmpty(
			summarysvc.ExtractString(route["source"]),
			summarysvc.ExtractString(route["source_file"]),
			summarysvc.ExtractString(route["file"]),
		)

		if method != "" && routeMethod != method {
			continue
		}
		if source != "" && !strings.Contains(strings.ToLower(routeSource), source) {
			continue
		}
		if pathFilter != "" && !strings.Contains(strings.ToLower(routePath), pathFilter) {
			continue
		}
		filtered = append(filtered, route)
	}

	sort.Slice(filtered, func(i, j int) bool {
		leftMethod := strings.ToUpper(summarysvc.ExtractString(filtered[i]["method"]))
		rightMethod := strings.ToUpper(summarysvc.ExtractString(filtered[j]["method"]))
		if leftMethod != rightMethod {
			return leftMethod < rightMethod
		}
		leftPath := summarysvc.ExtractString(filtered[i]["path"])
		rightPath := summarysvc.ExtractString(filtered[j]["path"])
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		return summarysvc.ExtractString(filtered[i]["source"]) < summarysvc.ExtractString(filtered[j]["source"])
	})

	payload := map[string]any{
		"total":  len(filtered),
		"offset": offset,
		"limit":  limit,
		"items":  paginateFindingMaps(filtered, offset, limit),
	}
	return marshalIndentedOrError(payload)
}

func ExecuteQueryStageOutput(task *model.Task, currentStage *model.TaskStage, stage, origin, verificationStatus string, offset, limit int) string {
	stageName := strings.TrimSpace(stage)
	if stageName == "" && currentStage != nil {
		stageName = currentStage.Name
	}
	if stageName == "" {
		return "Error: stage is required when no current stage context is available."
	}
	if stageName == "init" {
		return "Error: init stage does not expose findings. Use query_routes instead."
	}
	if limit <= 0 {
		limit = manifestDefaultQueryLimit
	}
	if offset < 0 {
		offset = 0
	}

	var stageRecord model.TaskStage
	switch {
	case currentStage != nil && currentStage.Name == stageName:
		stageRecord = *currentStage
	default:
		if err := database.DB.Where("task_id = ? AND name = ?", task.ID, stageName).First(&stageRecord).Error; err != nil {
			return fmt.Sprintf("Error loading stage %q: %v", stageName, err)
		}
	}

	findings, ok := summarysvc.ParseJSONArray(stageRecord.OutputJSON, stageRecord.Result)
	if !ok {
		return fmt.Sprintf("Stage %q output is not available as structured JSON yet.", stageName)
	}

	origin = strings.ToLower(strings.TrimSpace(origin))
	verificationStatus = strings.ToLower(strings.TrimSpace(verificationStatus))
	filtered := make([]map[string]any, 0, len(findings))
	for _, finding := range findings {
		findingOrigin := strings.ToLower(strings.TrimSpace(summarysvc.ExtractString(finding["origin"])))
		if findingOrigin == "" {
			findingOrigin = "initial"
		}
		status := summarysvc.FindingVerificationStatus(finding)

		if origin != "" && findingOrigin != origin {
			continue
		}
		if verificationStatus != "" && status != verificationStatus {
			continue
		}
		filtered = append(filtered, finding)
	}

	payload := map[string]any{
		"stage":  stageName,
		"total":  len(filtered),
		"offset": offset,
		"limit":  limit,
		"items":  paginateFindingMaps(filtered, offset, limit),
	}
	return marshalIndentedOrError(payload)
}

func BuildKnownRoutesContext(task *model.Task, manifest *ProjectManifest) string {
	routes, ok := summarysvc.ParseJSONArray(task.OutputJSON, task.Result)
	if !ok || len(routes) == 0 {
		return strings.TrimSpace(`Route inventory summary:
- total_routes: 0
- module_distribution: none
- query_routes: query_routes({"offset":0,"limit":20})
- query_manifest: query_manifest({"stage":"init","limit":20})`)
	}

	moduleCounts := map[string]int{}
	moduleRoots := []string{"."}
	if manifest != nil && len(manifest.ModuleRoots) > 0 {
		moduleRoots = manifest.ModuleRoots
	}
	for _, route := range routes {
		source := firstNonEmpty(
			summarysvc.ExtractString(route["source"]),
			summarysvc.ExtractString(route["source_file"]),
			summarysvc.ExtractString(route["file"]),
		)
		module := matchModuleRoot(moduleRoots, source)
		moduleCounts[module]++
	}

	lines := []string{
		"Route inventory summary:",
		fmt.Sprintf("- total_routes: %d", len(routes)),
		"- module_distribution:",
	}
	for _, module := range sortedCountKeys(moduleCounts) {
		lines = append(lines, fmt.Sprintf("  - %s: %d", module, moduleCounts[module]))
	}
	lines = append(lines,
		`- query_routes: query_routes({"method":"GET","path":"/api","offset":0,"limit":20})`,
		`- query_manifest: query_manifest({"stage":"auth","module":"backend","limit":20})`,
	)
	return strings.Join(lines, "\n")
}

func BuildCurrentFindingsContext(stage string, findings []map[string]any) string {
	if len(findings) == 0 {
		return strings.TrimSpace(fmt.Sprintf(`Current %s findings summary:
- total_findings: 0
- verification_status: none
- query_stage_output: query_stage_output({"stage":"%s","offset":0,"limit":20})`, summarysvc.StageLabel(stage), stage))
	}

	originCounts := map[string]int{}
	statusCounts := map[string]int{}
	for _, finding := range findings {
		origin := strings.ToLower(strings.TrimSpace(summarysvc.ExtractString(finding["origin"])))
		if origin == "" {
			origin = "initial"
		}
		originCounts[origin]++
		statusCounts[summarysvc.FindingVerificationStatus(finding)]++
	}

	lines := []string{
		fmt.Sprintf("Current %s findings summary:", summarysvc.StageLabel(stage)),
		fmt.Sprintf("- total_findings: %d", len(findings)),
		"- origin_distribution:",
	}
	for _, key := range sortedCountKeys(originCounts) {
		lines = append(lines, fmt.Sprintf("  - %s: %d", key, originCounts[key]))
	}
	lines = append(lines, "- verification_status_distribution:")
	for _, key := range sortedCountKeys(statusCounts) {
		lines = append(lines, fmt.Sprintf("  - %s: %d", key, statusCounts[key]))
	}
	lines = append(lines, fmt.Sprintf(`- query_stage_output: query_stage_output({"stage":"%s","origin":"gap_check","verification_status":"confirmed","offset":0,"limit":20})`, stage))
	return strings.Join(lines, "\n")
}

func appendStructuredQueryGuidance(prompt string, stage string) string {
	guidance := `

Structured Query Tools:
- query_manifest helps you inspect detected languages, framework hints, module roots, route candidates, and stage hotspots without loading the full manifest into context.
- query_routes helps you inspect structured route subsets by method, path, or source file instead of repeating the full route inventory.
- query_stage_output helps you inspect structured findings for the current stage during gap-check or revalidation without loading the full findings array.
- Prefer these query tools before re-reading large route or finding collections.`
	if stage == "init" {
		guidance += `
- For the init stage, start with query_manifest({"stage":"init","limit":20}) to focus route discovery on candidate files.`
	}
	return strings.TrimSpace(prompt) + guidance
}

func detectLanguage(name, relPath string) string {
	if language := languageByExtension[strings.ToLower(filepath.Ext(relPath))]; language != "" {
		return language
	}
	switch strings.ToLower(name) {
	case "go.mod":
		return "go"
	case "package.json":
		return "javascript"
	case "pom.xml", "build.gradle", "build.gradle.kts":
		return "java"
	case "requirements.txt", "pyproject.toml", "pipfile":
		return "python"
	case "composer.json":
		return "php"
	case "cargo.toml":
		return "rust"
	default:
		return ""
	}
}

func manifestModuleDir(relPath string) string {
	dir := filepath.ToSlash(filepath.Dir(relPath))
	if dir == "." || dir == "" {
		return "."
	}
	return dir
}

func looksBinary(content []byte) bool {
	for _, b := range content {
		if b == 0 {
			return true
		}
	}
	return false
}

func collectManifestLineHits(target map[string]*manifestFileAccumulator, relPath string, rules []manifestRule, line string, lineNo int) {
	for _, rule := range rules {
		if !rule.Pattern.MatchString(line) {
			continue
		}
		entry := ensureManifestAccumulator(target, relPath)
		hits := entry.Rules[rule.Name]
		if len(hits) < manifestMaxLinesPerRule {
			entry.Rules[rule.Name] = append(hits, lineNo)
		}
	}
}

func ensureManifestAccumulator(target map[string]*manifestFileAccumulator, relPath string) *manifestFileAccumulator {
	if existing, ok := target[relPath]; ok {
		return existing
	}
	entry := &manifestFileAccumulator{
		Path:  relPath,
		Rules: map[string][]int{},
	}
	target[relPath] = entry
	return entry
}

func finalizeManifestFiles(raw map[string]*manifestFileAccumulator, moduleRoots []string) []ProjectManifestIndexedFile {
	items := make([]ProjectManifestIndexedFile, 0, len(raw))
	for _, entry := range raw {
		file := ProjectManifestIndexedFile{
			Module: matchModuleRoot(moduleRoots, entry.Path),
			Path:   entry.Path,
			Rules:  make([]ProjectManifestRuleHits, 0, len(entry.Rules)),
		}
		for ruleName, lines := range entry.Rules {
			file.Rules = append(file.Rules, ProjectManifestRuleHits{
				Name:  ruleName,
				Lines: uniqueSortedInts(lines),
			})
		}
		sort.Slice(file.Rules, func(i, j int) bool {
			return file.Rules[i].Name < file.Rules[j].Name
		})
		items = append(items, file)
	}

	sort.Slice(items, func(i, j int) bool {
		leftHits := totalManifestHits(items[i])
		rightHits := totalManifestHits(items[j])
		if leftHits != rightHits {
			return leftHits > rightHits
		}
		return items[i].Path < items[j].Path
	})

	if len(items) > manifestMaxFilesPerSection {
		items = items[:manifestMaxFilesPerSection]
	}
	return items
}

func flattenManifestFiles(files []ProjectManifestIndexedFile, section, stage, moduleFilter, ruleFilter string) []manifestQueryItem {
	moduleFilter = strings.ToLower(strings.TrimSpace(moduleFilter))
	ruleFilter = strings.ToLower(strings.TrimSpace(ruleFilter))

	items := []manifestQueryItem{}
	for _, file := range files {
		if moduleFilter != "" && strings.ToLower(file.Module) != moduleFilter {
			continue
		}
		for _, rule := range file.Rules {
			if ruleFilter != "" && !strings.Contains(strings.ToLower(rule.Name), ruleFilter) {
				continue
			}
			items = append(items, manifestQueryItem{
				Section:  section,
				Stage:    stage,
				Module:   file.Module,
				Path:     file.Path,
				RuleName: rule.Name,
				Lines:    rule.Lines,
			})
		}
	}
	return items
}

func paginateManifestItems(items []manifestQueryItem, offset, limit int) []manifestQueryItem {
	if offset >= len(items) {
		return []manifestQueryItem{}
	}
	end := len(items)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return items[offset:end]
}

func paginateFindingMaps(items []map[string]any, offset, limit int) []map[string]any {
	if offset >= len(items) {
		return []map[string]any{}
	}
	end := len(items)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return items[offset:end]
}

func marshalIndentedOrError(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error marshalling query output: %v", err)
	}
	return string(data)
}

func matchModuleRoot(moduleRoots []string, relPath string) string {
	best := "."
	bestDepth := -1
	normalized := filepath.ToSlash(strings.TrimSpace(relPath))
	for _, root := range moduleRoots {
		root = filepath.ToSlash(strings.TrimSpace(root))
		switch {
		case root == ".":
			if bestDepth < 0 {
				best = "."
				bestDepth = 0
			}
		case normalized == root || strings.HasPrefix(normalized, root+"/"):
			depth := strings.Count(root, "/") + 1
			if depth > bestDepth {
				best = root
				bestDepth = depth
			}
		}
	}
	return best
}

func totalManifestHits(file ProjectManifestIndexedFile) int {
	total := 0
	for _, rule := range file.Rules {
		total += len(rule.Lines)
	}
	return total
}

func uniqueSortedInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	sort.Ints(values)
	out := values[:0]
	prev := -1
	for _, value := range values {
		if len(out) == 0 || value != prev {
			out = append(out, value)
			prev = value
		}
	}
	return out
}

func sortedStringKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedCountKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if values[keys[i]] != values[keys[j]] {
			return values[keys[i]] > values[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

func orderedAuditStages() []string {
	return []string{"rce", "injection", "auth", "access", "xss", "config", "fileop", "logic"}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
