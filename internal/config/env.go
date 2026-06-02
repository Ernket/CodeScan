package config

import (
	"os"
	"strconv"
	"strings"
)

func ApplyEnvOverrides(cfg Config) Config {
	if v := os.Getenv("CODESCAN_AUTH_KEY"); v != "" {
		cfg.AuthKey = v
	}
	if v := os.Getenv("CODESCAN_DB_HOST"); v != "" {
		cfg.DBConfig.Host = v
	}
	if v := os.Getenv("CODESCAN_DB_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.DBConfig.Port = port
		}
	}
	if v := os.Getenv("CODESCAN_DB_USER"); v != "" {
		cfg.DBConfig.User = v
	}
	if v := os.Getenv("CODESCAN_DB_PASSWORD"); v != "" {
		cfg.DBConfig.Password = v
	}
	if v := os.Getenv("CODESCAN_DB_NAME"); v != "" {
		cfg.DBConfig.DBName = v
	}
	if v := os.Getenv("CODESCAN_AI_API_KEY"); v != "" {
		cfg.AIConfig.APIKey = v
	}
	if v := os.Getenv("CODESCAN_AI_BASE_URL"); v != "" {
		cfg.AIConfig.BaseURL = v
	}
	if v := os.Getenv("CODESCAN_AI_MODEL"); v != "" {
		cfg.AIConfig.Model = v
	}
	if v := os.Getenv("CODESCAN_AI_THINKING_ENABLED"); v != "" {
		if parsed, ok := parseBoolEnv(v); ok {
			cfg.AIConfig.Thinking.Enabled = parsed
		}
	}
	if v := os.Getenv("CODESCAN_AI_REASONING_EFFORT"); v != "" {
		cfg.AIConfig.Thinking.Effort = v
	}
	if v := os.Getenv("CODESCAN_AI_MAX_COMPLETION_TOKENS"); v != "" {
		if tokens, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			cfg.AIConfig.Thinking.MaxCompletionTokens = tokens
		}
	}
	if v := os.Getenv("CODESCAN_AI_THINKING_APPLY_TO_AUXILIARY"); v != "" {
		if parsed, ok := parseBoolEnv(v); ok {
			cfg.AIConfig.Thinking.ApplyToAuxiliary = parsed
		}
	}

	return cfg
}

func parseBoolEnv(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, true
	case "0", "f", "false", "n", "no", "off":
		return false, true
	default:
		return false, false
	}
}
