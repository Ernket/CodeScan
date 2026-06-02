package config

import "encoding/json"

const ProjectsDir = "projects"

const (
	defaultContextWindowTokens    = 128000
	defaultSummaryWindowMessages  = 12
	defaultMicrocompactKeepRecent = 2
	defaultCompactMinTailMessages = 4
)

type DBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
}

type AIConfig struct {
	APIKey   string           `json:"api_key"`
	BaseURL  string           `json:"base_url"`
	Model    string           `json:"model"`
	Thinking AIThinkingConfig `json:"thinking"`
}

type AIThinkingConfig struct {
	Enabled             bool   `json:"enabled"`
	Effort              string `json:"effort"`
	MaxCompletionTokens int    `json:"max_completion_tokens"`
	ApplyToAuxiliary    bool   `json:"apply_to_auxiliary"`
}

type ContextCompressionConfig struct {
	ContextWindowTokens      int  `json:"context_window_tokens"`
	SummaryReservedTokens    int  `json:"-"`
	SafetyBufferTokens       int  `json:"-"`
	MicrocompactLimitTokens  int  `json:"-"`
	FullCompactLimitTokens   int  `json:"-"`
	HardLimitTokens          int  `json:"-"`
	TargetAfterCompactTokens int  `json:"-"`
	HardLimitBytes           int  `json:"-"`
	SummaryWindowMessages    int  `json:"-"`
	MicrocompactKeepRecent   int  `json:"-"`
	CompactMinTailMessages   int  `json:"-"`
	SessionMemoryEnabled     bool `json:"-"`
}

type SessionMemoryConfig struct {
	Enabled                bool `json:"enabled"`
	MinGrowthBytes         int  `json:"-"`
	MinToolCalls           int  `json:"-"`
	MaxUpdateBytes         int  `json:"-"`
	FailureCooldownSeconds int  `json:"-"`

	enabledSet bool
}

type ScannerConfig struct {
	ContextCompression ContextCompressionConfig `json:"context_compression"`
	SessionMemory      SessionMemoryConfig      `json:"session_memory"`
}

type ParallelRoleConfig struct {
	Parallelism int `json:"-"`
}

type ModelRoleConfig struct {
	Model       string `json:"model"`
	Parallelism int    `json:"-"`
}

type OrchestrationConfig struct {
	Enabled             bool               `json:"enabled"`
	SSEHeartbeatSeconds int                `json:"-"`
	Planner             ParallelRoleConfig `json:"-"`
	Worker              ModelRoleConfig    `json:"worker"`
	Integrator          ParallelRoleConfig `json:"-"`
	Validator           ModelRoleConfig    `json:"validator"`
	Persistence         ParallelRoleConfig `json:"-"`

	enabledSet bool
}

type Config struct {
	AuthKey             string              `json:"auth_key"`
	DBConfig            DBConfig            `json:"db_config"`
	AIConfig            AIConfig            `json:"ai_config"`
	ScannerConfig       ScannerConfig       `json:"scanner_config"`
	OrchestrationConfig OrchestrationConfig `json:"orchestration_config"`
}

func (c *SessionMemoryConfig) UnmarshalJSON(data []byte) error {
	type alias SessionMemoryConfig
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = SessionMemoryConfig(decoded)
	_, c.enabledSet = raw["enabled"]
	return nil
}

func (c *OrchestrationConfig) UnmarshalJSON(data []byte) error {
	type alias OrchestrationConfig
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = OrchestrationConfig(decoded)
	_, c.enabledSet = raw["enabled"]
	return nil
}

func DefaultScannerConfig() ScannerConfig {
	cfg := ScannerConfig{
		ContextCompression: ContextCompressionConfig{
			ContextWindowTokens:    defaultContextWindowTokens,
			SummaryWindowMessages:  defaultSummaryWindowMessages,
			MicrocompactKeepRecent: defaultMicrocompactKeepRecent,
			CompactMinTailMessages: defaultCompactMinTailMessages,
			SessionMemoryEnabled:   true,
		},
		SessionMemory: SessionMemoryConfig{
			Enabled:                true,
			MinGrowthBytes:         24 * 1024,
			MinToolCalls:           4,
			MaxUpdateBytes:         32 * 1024,
			FailureCooldownSeconds: 300,
		},
	}
	cfg.ContextCompression = deriveContextCompressionPolicy(cfg.ContextCompression)
	return cfg
}

func NormalizeScannerConfig(cfg ScannerConfig) (ScannerConfig, []string) {
	defaults := DefaultScannerConfig()
	warnings := []string{}

	if cfg.ContextCompression.SummaryWindowMessages <= 0 {
		cfg.ContextCompression.SummaryWindowMessages = defaults.ContextCompression.SummaryWindowMessages
	}
	if cfg.ContextCompression.MicrocompactKeepRecent <= 0 {
		cfg.ContextCompression.MicrocompactKeepRecent = defaults.ContextCompression.MicrocompactKeepRecent
	}
	if cfg.ContextCompression.CompactMinTailMessages <= 0 {
		cfg.ContextCompression.CompactMinTailMessages = defaults.ContextCompression.CompactMinTailMessages
	}
	if cfg.ContextCompression.ContextWindowTokens <= 0 {
		cfg.ContextCompression.ContextWindowTokens = defaults.ContextCompression.ContextWindowTokens
	}
	cfg.ContextCompression = deriveContextCompressionPolicy(cfg.ContextCompression)

	if !cfg.SessionMemory.enabledSet {
		cfg.SessionMemory.Enabled = defaults.SessionMemory.Enabled
	}
	if cfg.SessionMemory.MinGrowthBytes <= 0 {
		cfg.SessionMemory.MinGrowthBytes = defaults.SessionMemory.MinGrowthBytes
	}
	if cfg.SessionMemory.MinToolCalls <= 0 {
		cfg.SessionMemory.MinToolCalls = defaults.SessionMemory.MinToolCalls
	}
	if cfg.SessionMemory.MaxUpdateBytes <= 0 {
		cfg.SessionMemory.MaxUpdateBytes = defaults.SessionMemory.MaxUpdateBytes
	}
	if cfg.SessionMemory.FailureCooldownSeconds <= 0 {
		cfg.SessionMemory.FailureCooldownSeconds = defaults.SessionMemory.FailureCooldownSeconds
	}
	cfg.ContextCompression.SessionMemoryEnabled = cfg.SessionMemory.Enabled

	return cfg, warnings
}

func deriveContextCompressionPolicy(cfg ContextCompressionConfig) ContextCompressionConfig {
	if cfg.ContextWindowTokens <= 0 {
		cfg.ContextWindowTokens = defaultContextWindowTokens
	}

	summaryReserved := cfg.ContextWindowTokens * 12 / 100
	summaryReserved = clampInt(summaryReserved, 8000, 20000)

	safetyBuffer := cfg.ContextWindowTokens * 4 / 100
	safetyBuffer = clampInt(safetyBuffer, 4000, 12000)

	effectiveLimit := cfg.ContextWindowTokens - summaryReserved - safetyBuffer
	if effectiveLimit < 1 {
		effectiveLimit = cfg.ContextWindowTokens
	}
	if effectiveLimit < 1 {
		effectiveLimit = defaultContextWindowTokens - 20000 - 5120
	}

	cfg.SummaryReservedTokens = summaryReserved
	cfg.SafetyBufferTokens = safetyBuffer
	cfg.MicrocompactLimitTokens = effectiveLimit * 75 / 100
	cfg.FullCompactLimitTokens = effectiveLimit * 90 / 100
	cfg.HardLimitTokens = effectiveLimit
	cfg.TargetAfterCompactTokens = effectiveLimit * 55 / 100
	cfg.HardLimitBytes = cfg.HardLimitTokens * 4
	return cfg
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func DefaultOrchestrationConfig() OrchestrationConfig {
	return OrchestrationConfig{
		Enabled:             true,
		SSEHeartbeatSeconds: 15,
		Planner: ParallelRoleConfig{
			Parallelism: 1,
		},
		Worker: ModelRoleConfig{
			Parallelism: 4,
		},
		Integrator: ParallelRoleConfig{
			Parallelism: 2,
		},
		Validator: ModelRoleConfig{
			Parallelism: 2,
		},
		Persistence: ParallelRoleConfig{
			Parallelism: 1,
		},
	}
}

func NormalizeOrchestrationConfig(cfg OrchestrationConfig) OrchestrationConfig {
	defaults := DefaultOrchestrationConfig()

	if !cfg.enabledSet {
		cfg.Enabled = defaults.Enabled
	}
	if cfg.SSEHeartbeatSeconds <= 0 {
		cfg.SSEHeartbeatSeconds = defaults.SSEHeartbeatSeconds
	}

	cfg.Planner = normalizeParallelRoleConfig(cfg.Planner, defaults.Planner)
	cfg.Worker = normalizeModelRoleConfig(cfg.Worker, defaults.Worker)
	cfg.Integrator = normalizeParallelRoleConfig(cfg.Integrator, defaults.Integrator)
	cfg.Validator = normalizeModelRoleConfig(cfg.Validator, defaults.Validator)
	cfg.Persistence = normalizeParallelRoleConfig(cfg.Persistence, defaults.Persistence)

	return cfg
}

func normalizeParallelRoleConfig(cfg, defaults ParallelRoleConfig) ParallelRoleConfig {
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = defaults.Parallelism
	}
	return cfg
}

func normalizeModelRoleConfig(cfg, defaults ModelRoleConfig) ModelRoleConfig {
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = defaults.Parallelism
	}
	return cfg
}

// Global AI config accessible by scanner.
var AI AIConfig

// Global scanner config accessible by scanner.
var Scanner ScannerConfig

// Global orchestration config accessible by the orchestration service.
var Orchestration OrchestrationConfig
