package config

import (
	"errors"
	"strings"
	"testing"
)

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
	if cfg.ContextCompression.SoftLimitTokens != 22000 || cfg.ContextCompression.HardLimitTokens != 34000 {
		t.Fatalf("expected prompt token limits 22000/34000, got %d/%d", cfg.ContextCompression.SoftLimitTokens, cfg.ContextCompression.HardLimitTokens)
	}
}

func TestNormalizeScannerConfigRejectsTokenHardLimitBelowSoftLimit(t *testing.T) {
	cfg, warnings := NormalizeScannerConfig(ScannerConfig{
		ContextCompression: ContextCompressionConfig{
			SoftLimitTokens: 1000,
			HardLimitTokens: 900,
		},
	})

	if len(warnings) == 0 {
		t.Fatal("expected warning for invalid hard limit")
	}
	if cfg.ContextCompression.HardLimitTokens <= cfg.ContextCompression.SoftLimitTokens {
		t.Fatalf("expected token hard limit to be greater than soft limit, got soft=%d hard=%d", cfg.ContextCompression.SoftLimitTokens, cfg.ContextCompression.HardLimitTokens)
	}
	if !strings.Contains(warnings[0], "hard_limit_tokens") {
		t.Fatalf("expected hard_limit_tokens warning, got %v", warnings)
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
				"soft_limit_tokens": 24000,
				"hard_limit_tokens": 36000
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
	if normalized.ContextCompression.SoftLimitTokens != 24000 || normalized.ContextCompression.HardLimitTokens != 36000 {
		t.Fatalf("expected configured token limits to be preserved, got %+v", normalized.ContextCompression)
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
			"model": "gpt-5.4"
		},
		"scanner_config": {
			"context_compression": {
				"soft_limit_tokens": 22000,
				"hard_limit_tokens": 34000
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
	if normalizedScanner.ContextCompression.SoftLimitTokens != 22000 || normalizedScanner.ContextCompression.HardLimitTokens != 34000 {
		t.Fatalf("expected scanner token limits to decode, got %+v", normalizedScanner.ContextCompression)
	}
	if normalizedScanner.ContextCompression.HardLimitBytes != 140000 {
		t.Fatalf("expected scanner context hard limit to default to 140000, got %+v", normalizedScanner.ContextCompression)
	}
	if cfg.OrchestrationConfig.Worker.Model != "gpt-5.4" {
		t.Fatalf("expected worker model to decode, got %q", cfg.OrchestrationConfig.Worker.Model)
	}
	normalizedOrchestration := NormalizeOrchestrationConfig(cfg.OrchestrationConfig)
	if normalizedOrchestration.Planner.Parallelism != 1 {
		t.Fatalf("expected planner parallelism to default to 1, got %d", normalizedOrchestration.Planner.Parallelism)
	}
}
