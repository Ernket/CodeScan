package config

import "strings"

const (
	defaultAIModel           = "gemini-3-pro-high"
	defaultAIReasoningEffort = "high"
	aiReasoningEffortLow     = "low"
	aiReasoningEffortMedium  = "medium"
	aiReasoningEffortHigh    = "high"
)

func DefaultAIConfig() AIConfig {
	return AIConfig{
		Model:    defaultAIModel,
		Thinking: DefaultAIThinkingConfig(),
	}
}

func DefaultAIThinkingConfig() AIThinkingConfig {
	return AIThinkingConfig{
		Enabled: false,
		Effort:  defaultAIReasoningEffort,
	}
}

func NormalizeAIConfig(cfg AIConfig) (AIConfig, []string) {
	defaults := DefaultAIConfig()
	warnings := []string{}

	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = defaults.Model
	}

	effort := strings.ToLower(strings.TrimSpace(cfg.Thinking.Effort))
	switch effort {
	case "":
		cfg.Thinking.Effort = defaultAIReasoningEffort
	case aiReasoningEffortLow, aiReasoningEffortMedium, aiReasoningEffortHigh:
		cfg.Thinking.Effort = effort
	default:
		cfg.Thinking.Effort = defaultAIReasoningEffort
		warnings = append(warnings, "ai_config.thinking.effort must be one of low, medium, high; falling back to high")
	}

	if cfg.Thinking.MaxCompletionTokens < 0 {
		cfg.Thinking.MaxCompletionTokens = 0
		warnings = append(warnings, "ai_config.thinking.max_completion_tokens must be non-negative; ignoring configured value")
	}

	return cfg, warnings
}
