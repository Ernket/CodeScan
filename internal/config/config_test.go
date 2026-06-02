package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeAIConfigDefaults(t *testing.T) {
	cfg, warnings := NormalizeAIConfig(AIConfig{})

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for defaulted AI config, got %v", warnings)
	}
	if cfg.Model != "gemini-3-pro-high" {
		t.Fatalf("expected default model, got %q", cfg.Model)
	}
	if cfg.Thinking.Enabled {
		t.Fatal("expected thinking to default to disabled")
	}
	if cfg.Thinking.Effort != "high" {
		t.Fatalf("expected thinking effort to default to high, got %q", cfg.Thinking.Effort)
	}
	if cfg.Thinking.MaxCompletionTokens != 0 {
		t.Fatalf("expected max completion tokens to default to 0, got %d", cfg.Thinking.MaxCompletionTokens)
	}
	if cfg.Thinking.ApplyToAuxiliary {
		t.Fatal("expected thinking auxiliary application to default to false")
	}
}

func TestNormalizeAIConfigPreservesExplicitThinkingConfig(t *testing.T) {
	cfg, warnings := NormalizeAIConfig(AIConfig{
		Model: "gpt-5.4",
		Thinking: AIThinkingConfig{
			Enabled:             true,
			Effort:              "MEDIUM",
			MaxCompletionTokens: 12000,
			ApplyToAuxiliary:    true,
		},
	})

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if !cfg.Thinking.Enabled || cfg.Thinking.Effort != "medium" || cfg.Thinking.MaxCompletionTokens != 12000 || !cfg.Thinking.ApplyToAuxiliary {
		t.Fatalf("expected thinking config to be preserved and normalized, got %+v", cfg.Thinking)
	}
}

func TestNormalizeAIConfigDefaultsEmptyThinkingEffort(t *testing.T) {
	cfg, warnings := NormalizeAIConfig(AIConfig{
		Thinking: AIThinkingConfig{
			Enabled: true,
		},
	})

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if cfg.Thinking.Effort != "high" {
		t.Fatalf("expected empty effort to default to high, got %q", cfg.Thinking.Effort)
	}
}

func TestNormalizeAIConfigInvalidThinkingEffortWarns(t *testing.T) {
	cfg, warnings := NormalizeAIConfig(AIConfig{
		Thinking: AIThinkingConfig{
			Enabled: true,
			Effort:  "extreme",
		},
	})

	if cfg.Thinking.Effort != "high" {
		t.Fatalf("expected invalid effort to fall back to high, got %q", cfg.Thinking.Effort)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "thinking.effort") {
		t.Fatalf("expected effort warning, got %v", warnings)
	}
}

func TestApplyEnvOverridesAIThinking(t *testing.T) {
	t.Setenv("CODESCAN_AI_THINKING_ENABLED", "false")
	t.Setenv("CODESCAN_AI_REASONING_EFFORT", "low")
	t.Setenv("CODESCAN_AI_MAX_COMPLETION_TOKENS", "8192")
	t.Setenv("CODESCAN_AI_THINKING_APPLY_TO_AUXILIARY", "false")

	cfg := ApplyEnvOverrides(Config{
		AIConfig: AIConfig{
			Thinking: AIThinkingConfig{
				Enabled:             true,
				Effort:              "high",
				MaxCompletionTokens: 1,
				ApplyToAuxiliary:    true,
			},
		},
	})

	if cfg.AIConfig.Thinking.Enabled {
		t.Fatal("expected env to override thinking enabled to false")
	}
	if cfg.AIConfig.Thinking.Effort != "low" {
		t.Fatalf("expected env reasoning effort, got %q", cfg.AIConfig.Thinking.Effort)
	}
	if cfg.AIConfig.Thinking.MaxCompletionTokens != 8192 {
		t.Fatalf("expected env max completion tokens, got %d", cfg.AIConfig.Thinking.MaxCompletionTokens)
	}
	if cfg.AIConfig.Thinking.ApplyToAuxiliary {
		t.Fatal("expected env to override auxiliary thinking to false")
	}
}

func TestNormalizeScannerConfigDefaults(t *testing.T) {
	cfg, warnings := NormalizeScannerConfig(ScannerConfig{})

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for defaulted config, got %v", warnings)
	}
	if cfg != DefaultScannerConfig() {
		t.Fatalf("expected normalized config to match defaults, got %+v", cfg)
	}
	if cfg.SessionMemory.MaxUpdateBytes != 32*1024 {
		t.Fatalf("expected session memory max update bytes to default to 32768, got %d", cfg.SessionMemory.MaxUpdateBytes)
	}
	if cfg.SessionMemory.FailureCooldownSeconds != 300 {
		t.Fatalf("expected session memory failure cooldown to default to 300, got %d", cfg.SessionMemory.FailureCooldownSeconds)
	}
	if cfg.ContextCompression.ContextWindowTokens != 128000 {
		t.Fatalf("expected context window to default to 128000, got %d", cfg.ContextCompression.ContextWindowTokens)
	}
	if cfg.ContextCompression.SummaryReservedTokens != 15360 {
		t.Fatalf("expected summary reserved tokens 15360, got %d", cfg.ContextCompression.SummaryReservedTokens)
	}
	if cfg.ContextCompression.SafetyBufferTokens != 5120 {
		t.Fatalf("expected safety buffer tokens 5120, got %d", cfg.ContextCompression.SafetyBufferTokens)
	}
	if cfg.ContextCompression.MicrocompactLimitTokens != 80640 {
		t.Fatalf("expected microcompact limit 80640, got %d", cfg.ContextCompression.MicrocompactLimitTokens)
	}
	if cfg.ContextCompression.FullCompactLimitTokens != 96768 {
		t.Fatalf("expected full compact limit 96768, got %d", cfg.ContextCompression.FullCompactLimitTokens)
	}
	if cfg.ContextCompression.HardLimitTokens != 107520 {
		t.Fatalf("expected hard limit 107520, got %d", cfg.ContextCompression.HardLimitTokens)
	}
	if cfg.ContextCompression.TargetAfterCompactTokens != 59136 {
		t.Fatalf("expected target after compact 59136, got %d", cfg.ContextCompression.TargetAfterCompactTokens)
	}
	if cfg.ContextCompression.HardLimitBytes != 430080 {
		t.Fatalf("expected hard byte fallback 430080, got %d", cfg.ContextCompression.HardLimitBytes)
	}
}

func TestNormalizeScannerConfigDerivesContextPolicyFromWindow(t *testing.T) {
	cfg, warnings := NormalizeScannerConfig(ScannerConfig{
		ContextCompression: ContextCompressionConfig{
			ContextWindowTokens: 320000,
		},
	})

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if cfg.ContextCompression.SummaryReservedTokens != 20000 {
		t.Fatalf("expected summary reserved cap 20000, got %d", cfg.ContextCompression.SummaryReservedTokens)
	}
	if cfg.ContextCompression.SafetyBufferTokens != 12000 {
		t.Fatalf("expected safety buffer cap 12000, got %d", cfg.ContextCompression.SafetyBufferTokens)
	}
	if cfg.ContextCompression.MicrocompactLimitTokens != 216000 {
		t.Fatalf("expected microcompact limit 216000, got %d", cfg.ContextCompression.MicrocompactLimitTokens)
	}
	if cfg.ContextCompression.FullCompactLimitTokens != 259200 {
		t.Fatalf("expected full compact limit 259200, got %d", cfg.ContextCompression.FullCompactLimitTokens)
	}
	if cfg.ContextCompression.HardLimitTokens != 288000 {
		t.Fatalf("expected hard limit 288000, got %d", cfg.ContextCompression.HardLimitTokens)
	}
	if cfg.ContextCompression.TargetAfterCompactTokens != 158400 {
		t.Fatalf("expected target after compact 158400, got %d", cfg.ContextCompression.TargetAfterCompactTokens)
	}
	if cfg.ContextCompression.HardLimitBytes != 1152000 {
		t.Fatalf("expected hard byte fallback 1152000, got %d", cfg.ContextCompression.HardLimitBytes)
	}
}

func TestNormalizeScannerConfigPreservesExplicitFalseFlags(t *testing.T) {
	cfg, err := DecodeConfigJSON([]byte(`{
		"scanner_config": {
			"session_memory": {
				"enabled": false
			}
		}
	}`))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}

	normalized, warnings := NormalizeScannerConfig(cfg.ScannerConfig)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if normalized.ContextCompression.SessionMemoryEnabled {
		t.Fatal("expected context compression session memory gate to follow session_memory.enabled=false")
	}
	if normalized.SessionMemory.Enabled {
		t.Fatal("expected session_memory.enabled=false to be preserved")
	}
}

func TestNormalizeOrchestrationConfigDefaults(t *testing.T) {
	cfg := NormalizeOrchestrationConfig(OrchestrationConfig{})

	if !cfg.Enabled {
		t.Fatal("expected orchestration to default to enabled")
	}
	if cfg.Worker.Parallelism != 4 {
		t.Fatalf("expected worker parallelism 4, got %d", cfg.Worker.Parallelism)
	}
	if cfg.Integrator.Parallelism != 2 {
		t.Fatalf("expected integrator parallelism 2, got %d", cfg.Integrator.Parallelism)
	}
	if cfg.Validator.Parallelism != 2 {
		t.Fatalf("expected validator parallelism 2, got %d", cfg.Validator.Parallelism)
	}
	if cfg.Persistence.Parallelism != 1 {
		t.Fatalf("expected persistence parallelism 1, got %d", cfg.Persistence.Parallelism)
	}
}

func TestNormalizeOrchestrationConfigPreservesExplicitDisable(t *testing.T) {
	cfg, err := DecodeConfigJSON([]byte(`{
		"orchestration_config": {
			"enabled": false
		}
	}`))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}

	normalized := NormalizeOrchestrationConfig(cfg.OrchestrationConfig)
	if normalized.Enabled {
		t.Fatal("expected orchestration_config.enabled=false to be preserved")
	}
}

func TestDecodeConfigJSONRejectsUnknownTopLevelKey(t *testing.T) {
	_, err := DecodeConfigJSON([]byte(`{"unexpected": true}`))
	if err == nil {
		t.Fatal("expected unknown top-level key error")
	}

	var fieldErr *ConfigFieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected ConfigFieldError, got %T (%v)", err, err)
	}
	if fieldErr.Path != "unexpected" || fieldErr.Removed {
		t.Fatalf("unexpected field error %+v", fieldErr)
	}
}

func TestDecodeConfigJSONRejectsUnknownNestedKey(t *testing.T) {
	_, err := DecodeConfigJSON([]byte(`{
		"scanner_config": {
			"session_memory": {
				"unexpected": 2
			}
		}
	}`))
	if err == nil {
		t.Fatal("expected unknown nested key error")
	}

	var fieldErr *ConfigFieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected ConfigFieldError, got %T (%v)", err, err)
	}
	if fieldErr.Path != "scanner_config.session_memory.unexpected" || fieldErr.Removed {
		t.Fatalf("unexpected field error %+v", fieldErr)
	}
}

func TestDecodeConfigJSONAcceptsMinimalContextCompressionConfig(t *testing.T) {
	cfg, err := DecodeConfigJSON([]byte(`{
		"scanner_config": {
			"context_compression": {
				"context_window_tokens": 320000
			}
		}
	}`))
	if err != nil {
		t.Fatalf("expected minimal context compression config to decode, got %v", err)
	}

	normalized, warnings := NormalizeScannerConfig(cfg.ScannerConfig)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if normalized.ContextCompression.ContextWindowTokens != 320000 {
		t.Fatalf("expected configured context window to be preserved, got %+v", normalized.ContextCompression)
	}
	if normalized.ContextCompression.HardLimitTokens != 288000 || normalized.ContextCompression.FullCompactLimitTokens != 259200 {
		t.Fatalf("expected derived token policy for 320000 window, got %+v", normalized.ContextCompression)
	}
}

func TestDecodeConfigJSONRejectsRemovedLegacyContextCompressionTokenFields(t *testing.T) {
	_, err := DecodeConfigJSON([]byte(`{
		"scanner_config": {
			"context_compression": {
				"soft_limit_tokens": 24000
			}
		}
	}`))
	if err == nil {
		t.Fatal("expected removed legacy token field error")
	}

	var fieldErr *ConfigFieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected ConfigFieldError, got %T (%v)", err, err)
	}
	if fieldErr.Path != "scanner_config.context_compression.soft_limit_tokens" || !fieldErr.Removed {
		t.Fatalf("unexpected field error %+v", fieldErr)
	}
	if !strings.Contains(err.Error(), `context_window_tokens`) {
		t.Fatalf("expected context_window_tokens replacement hint in error, got %v", err)
	}
}

func TestDecodeConfigJSONRejectsRemovedLegacyContextCompressionByteField(t *testing.T) {
	_, err := DecodeConfigJSON([]byte(`{
		"scanner_config": {
			"context_compression": {
				"soft_limit_bytes": 90000
			}
		}
	}`))
	if err == nil {
		t.Fatal("expected removed legacy field error")
	}

	var fieldErr *ConfigFieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected ConfigFieldError, got %T (%v)", err, err)
	}
	if fieldErr.Path != "scanner_config.context_compression.soft_limit_bytes" || !fieldErr.Removed {
		t.Fatalf("unexpected field error %+v", fieldErr)
	}
	if !strings.Contains(err.Error(), `byte fallback threshold is now built in`) {
		t.Fatalf("expected replacement hint in error, got %v", err)
	}
}

func TestDecodeConfigJSONRejectsRemovedWorkerParallelismField(t *testing.T) {
	_, err := DecodeConfigJSON([]byte(`{
		"orchestration_config": {
			"worker": {
				"parallelism": 4
			}
		}
	}`))
	if err == nil {
		t.Fatal("expected removed worker.parallelism field error")
	}

	var fieldErr *ConfigFieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected ConfigFieldError, got %T (%v)", err, err)
	}
	if fieldErr.Path != "orchestration_config.worker.parallelism" || !fieldErr.Removed {
		t.Fatalf("unexpected field error %+v", fieldErr)
	}
	if !strings.Contains(err.Error(), `worker fan-out is now built in`) {
		t.Fatalf("expected built-in fan-out hint in error, got %v", err)
	}
}

func TestDecodeConfigJSONAcceptsCurrentSchema(t *testing.T) {
	cfg, err := DecodeConfigJSON([]byte(`{
		"auth_key": "token",
		"db_config": {
			"host": "127.0.0.1",
			"port": 3306,
			"user": "root",
			"password": "secret",
			"dbname": "codescan"
		},
		"ai_config": {
			"api_key": "k",
			"base_url": "https://api.openai.com/v1",
			"model": "gpt-5.4",
			"thinking": {
				"enabled": true,
				"effort": "high",
				"max_completion_tokens": 20000,
				"apply_to_auxiliary": true
			}
		},
		"scanner_config": {
			"context_compression": {
				"context_window_tokens": 128000
			},
			"session_memory": {
				"enabled": true
			}
		},
		"orchestration_config": {
			"enabled": true,
			"worker": {
				"model": "gpt-5.4"
			},
			"validator": {
				"model": "gpt-5.4-mini"
			}
		}
	}`))
	if err != nil {
		t.Fatalf("expected current schema to decode, got %v", err)
	}

	normalizedScanner, warnings := NormalizeScannerConfig(cfg.ScannerConfig)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if normalizedScanner.ContextCompression.MicrocompactLimitTokens != 80640 || normalizedScanner.ContextCompression.FullCompactLimitTokens != 96768 || normalizedScanner.ContextCompression.HardLimitTokens != 107520 {
		t.Fatalf("expected scanner context policy to decode, got %+v", normalizedScanner.ContextCompression)
	}
	if normalizedScanner.ContextCompression.HardLimitBytes != 430080 {
		t.Fatalf("expected scanner context hard byte fallback 430080, got %+v", normalizedScanner.ContextCompression)
	}
	if cfg.OrchestrationConfig.Worker.Model != "gpt-5.4" {
		t.Fatalf("expected worker model to decode, got %q", cfg.OrchestrationConfig.Worker.Model)
	}
	if !cfg.AIConfig.Thinking.Enabled || cfg.AIConfig.Thinking.Effort != "high" || cfg.AIConfig.Thinking.MaxCompletionTokens != 20000 || !cfg.AIConfig.Thinking.ApplyToAuxiliary {
		t.Fatalf("expected AI thinking config to decode, got %+v", cfg.AIConfig.Thinking)
	}
	normalizedOrchestration := NormalizeOrchestrationConfig(cfg.OrchestrationConfig)
	if normalizedOrchestration.Planner.Parallelism != 1 {
		t.Fatalf("expected planner parallelism to default to 1, got %d", normalizedOrchestration.Planner.Parallelism)
	}
}

func TestLoadConfigFileEnablesConfiguredThinking(t *testing.T) {
	cfg, err := LoadConfigFile(filepath.Join("..", "..", "data", "config.json"))
	if err != nil {
		t.Fatalf("load config file: %v", err)
	}

	normalized, warnings := NormalizeAIConfig(cfg.AIConfig)
	if len(warnings) != 0 {
		t.Fatalf("expected no AI config warnings, got %v", warnings)
	}
	if !normalized.Thinking.Enabled || normalized.Thinking.Effort != "high" || normalized.Thinking.MaxCompletionTokens != 200000 || !normalized.Thinking.ApplyToAuxiliary {
		t.Fatalf("expected active high-effort thinking config, got %+v", normalized.Thinking)
	}
}
