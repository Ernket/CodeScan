package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tmc/langchaingo/llms"
	lcopenai "github.com/tmc/langchaingo/llms/openai"
	lctools "github.com/tmc/langchaingo/tools"

	"codescan/internal/config"
	"codescan/internal/service/scanner"
)

type ArtifactProvider interface {
	LoadArtifact(id string) (string, error)
	LoadEvidence(id string) (string, error)
}

type ToolContext struct {
	BasePath  string
	Artifacts ArtifactProvider
}

type langChainTool struct {
	name        string
	description string
	call        func(ctx context.Context, input string) (string, error)
}

func (t langChainTool) Name() string {
	return t.name
}

func (t langChainTool) Description() string {
	return t.description
}

func (t langChainTool) Call(ctx context.Context, input string) (string, error) {
	return t.call(ctx, input)
}

func NewLangChainModel(role string) (llms.Model, error) {
	roleCfg := roleConfig(role)
	options := []lcopenai.Option{
		lcopenai.WithToken(config.AI.APIKey),
		lcopenai.WithModel(roleCfg.Model),
	}
	if strings.TrimSpace(config.AI.BaseURL) != "" {
		options = append(options, lcopenai.WithBaseURL(config.AI.BaseURL))
	}
	return lcopenai.New(options...)
}

func NewLangChainTools(ctx ToolContext) []lctools.Tool {
	tools := []lctools.Tool{
		langChainTool{
			name:        "read_file",
			description: `Read a file snippet. Input JSON: {"path":"relative/path","start_line":1,"end_line":120,"max_output_bytes":16384}.`,
			call: func(_ context.Context, input string) (string, error) {
				var req struct {
					Path           string `json:"path"`
					StartLine      int    `json:"start_line"`
					EndLine        int    `json:"end_line"`
					MaxOutputBytes int    `json:"max_output_bytes"`
				}
				if err := json.Unmarshal([]byte(input), &req); err != nil {
					return "", err
				}
				path, err := resolveToolPath(ctx.BasePath, req.Path)
				if err != nil {
					return "", err
				}
				return scanner.ExecuteReadFile(path, req.StartLine, req.EndLine, req.MaxOutputBytes), nil
			},
		},
		langChainTool{
			name:        "list_files",
			description: `List files under a directory. Input JSON: {"path":"relative/path","max_entries":200}.`,
			call: func(_ context.Context, input string) (string, error) {
				var req struct {
					Path       string `json:"path"`
					MaxEntries int    `json:"max_entries"`
				}
				if err := json.Unmarshal([]byte(input), &req); err != nil {
					return "", err
				}
				path, err := resolveToolPath(ctx.BasePath, req.Path)
				if err != nil {
					return "", err
				}
				return scanner.ExecuteListFiles(path, req.MaxEntries), nil
			},
		},
		langChainTool{
			name:        "list_dir_tree",
			description: `List a directory tree. Input JSON: {"path":"relative/path","max_depth":3,"max_entries":200}.`,
			call: func(_ context.Context, input string) (string, error) {
				var req struct {
					Path       string `json:"path"`
					MaxDepth   int    `json:"max_depth"`
					MaxEntries int    `json:"max_entries"`
				}
				if err := json.Unmarshal([]byte(input), &req); err != nil {
					return "", err
				}
				path, err := resolveToolPath(ctx.BasePath, req.Path)
				if err != nil {
					return "", err
				}
				return scanner.ExecuteListDirTree(path, req.MaxDepth, req.MaxEntries), nil
			},
		},
		langChainTool{
			name:        "search_files",
			description: `Search for files by name. Input JSON: {"path":"relative/path","pattern":"*.go","max_results":100,"offset":0}.`,
			call: func(_ context.Context, input string) (string, error) {
				var req struct {
					Path       string `json:"path"`
					Pattern    string `json:"pattern"`
					MaxResults int    `json:"max_results"`
					Offset     int    `json:"offset"`
				}
				if err := json.Unmarshal([]byte(input), &req); err != nil {
					return "", err
				}
				path, err := resolveToolPath(ctx.BasePath, req.Path)
				if err != nil {
					return "", err
				}
				return scanner.ExecuteSearchFiles(path, req.Pattern, req.MaxResults, req.Offset), nil
			},
		},
		langChainTool{
			name:        "grep_files",
			description: `Regex grep over files. Input JSON: {"path":"relative/path","pattern":"router.GET","case_insensitive":false,"max_results":80,"offset":0,"max_files":80,"max_output_bytes":32768}.`,
			call: func(_ context.Context, input string) (string, error) {
				var req struct {
					Path            string `json:"path"`
					Pattern         string `json:"pattern"`
					CaseInsensitive bool   `json:"case_insensitive"`
					MaxResults      int    `json:"max_results"`
					Offset          int    `json:"offset"`
					MaxFiles        int    `json:"max_files"`
					MaxOutputBytes  int    `json:"max_output_bytes"`
				}
				if err := json.Unmarshal([]byte(input), &req); err != nil {
					return "", err
				}
				path, err := resolveToolPath(ctx.BasePath, req.Path)
				if err != nil {
					return "", err
				}
				return scanner.ExecuteGrepFiles(path, req.Pattern, req.CaseInsensitive, req.MaxResults, req.Offset, req.MaxFiles, req.MaxOutputBytes), nil
			},
		},
	}

	if ctx.Artifacts != nil {
		tools = append(tools,
			langChainTool{
				name:        "get_artifact",
				description: `Load a persisted artifact by ID. Input JSON: {"artifact_id":"art-000001"}.`,
				call: func(_ context.Context, input string) (string, error) {
					var req struct {
						ArtifactID string `json:"artifact_id"`
					}
					if err := json.Unmarshal([]byte(input), &req); err != nil {
						return "", err
					}
					return ctx.Artifacts.LoadArtifact(req.ArtifactID)
				},
			},
			langChainTool{
				name:        "get_evidence",
				description: `Load a persisted evidence record by ID. Input JSON: {"evidence_id":"art-000001"}.`,
				call: func(_ context.Context, input string) (string, error) {
					var req struct {
						EvidenceID string `json:"evidence_id"`
					}
					if err := json.Unmarshal([]byte(input), &req); err != nil {
						return "", err
					}
					return ctx.Artifacts.LoadEvidence(req.EvidenceID)
				},
			},
		)
	}

	return tools
}

func resolveToolPath(basePath, relativePath string) (string, error) {
	base := filepath.Clean(basePath)
	if base == "." || base == "" {
		return "", fmt.Errorf("base path is not configured")
	}

	target := filepath.Clean(filepath.Join(base, relativePath))
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", relativePath)
	}
	return target, nil
}
