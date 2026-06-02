package orchestration

import (
	"testing"

	"codescan/internal/config"

	"github.com/tmc/langchaingo/llms"
)

func TestLangChainThinkingOptionsDisabled(t *testing.T) {
	restore := setLangChainTestAIConfig(t, config.AIConfig{
		Thinking: config.AIThinkingConfig{
			Enabled: false,
			Effort:  "high",
		},
	})
	defer restore()

	options := LangChainThinkingOptions()

	if len(options) != 0 {
		t.Fatalf("expected no LangChain thinking options when disabled, got %d", len(options))
	}
}

func TestLangChainThinkingOptionsApplyHighEffortAndMaxCompletionTokens(t *testing.T) {
	restore := setLangChainTestAIConfig(t, config.AIConfig{
		Thinking: config.AIThinkingConfig{
			Enabled:             true,
			Effort:              "high",
			MaxCompletionTokens: 20000,
			ApplyToAuxiliary:    true,
		},
	})
	defer restore()

	var callOptions llms.CallOptions
	for _, option := range LangChainThinkingOptions() {
		option(&callOptions)
	}

	thinking := llms.GetThinkingConfig(&callOptions)
	if thinking == nil {
		t.Fatal("expected thinking config metadata")
	}
	if thinking.Mode != llms.ThinkingModeHigh {
		t.Fatalf("expected high thinking mode, got %q", thinking.Mode)
	}
	if callOptions.MaxTokens != 20000 {
		t.Fatalf("expected max completion tokens to map to MaxTokens=20000, got %d", callOptions.MaxTokens)
	}
}

func setLangChainTestAIConfig(t *testing.T, cfg config.AIConfig) func() {
	t.Helper()

	previous := config.AI
	config.AI = cfg

	return func() {
		config.AI = previous
	}
}
