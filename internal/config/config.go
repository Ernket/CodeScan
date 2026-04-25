package config

import "encoding/json"

const ProjectsDir = "projects"

type DBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
}

type AIConfig struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

type ContextCompressionConfig struct {
	SoftLimitTokens        int  `json:"soft_limit_tokens"`
	HardLimitTokens        int  `json:"hard_limit_tokens"`
	SoftLimitBytes         int  `json:"-"`
	HardLimitBytes         int  `json:"-"`
	SummaryWindowMessages  int  `json:"-"`
	MicrocompactKeepRecent int  `json:"-"`
	CompactMinTailMessages int  `json:"-"`
	SessionMemoryEnabled   bool `json:"-"`
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
	return ScannerConfig{
		ContextCompression: ContextCompressionConfig{
			SoftLimitTokens:        22000,
			HardLimitTokens:        34000,
			SoftLimitBytes:         90000,
			HardLimitBytes:         140000,
			SummaryWindowMessages:  12,
			MicrocompactKeepRecent: 2,
			CompactMinTailMessages: 4,
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
}

func NormalizeScannerConfig(cfg ScannerConfig) (ScannerConfig, []string) {
	defaults := DefaultScannerConfig()
	warnings := []string{}

	if cfg.ContextCompression.SoftLimitTokens <= 0 {
		cfg.ContextCompression.SoftLimitTokens = defaults.ContextCompression.SoftLimitTokens
	}
	if cfg.ContextCompression.HardLimitTokens <= 0 {
		cfg.ContextCompression.HardLimitTokens = defaults.ContextCompression.HardLimitTokens
	}
	if cfg.ContextCompression.HardLimitTokens <= cfg.ContextCompression.SoftLimitTokens {
		cfg.ContextCompression.HardLimitTokens = defaults.ContextCompression.HardLimitTokens
		if cfg.ContextCompression.HardLimitTokens <= cfg.ContextCompression.SoftLimitTokens {
			cfg.ContextCompression.HardLimitTokens = cfg.ContextCompression.SoftLimitTokens + 1
		}
		warnings = append(warnings, "scanner_config.context_compression.hard_limit_tokens must be greater than soft_limit_tokens; falling back to a safe hard limit")
	}
	if cfg.ContextCompression.SoftLimitBytes <= 0 {
		cfg.ContextCompression.SoftLimitBytes = defaults.ContextCompression.SoftLimitBytes
	}
	if cfg.ContextCompression.HardLimitBytes <= 0 {
		cfg.ContextCompression.HardLimitBytes = defaults.ContextCompression.HardLimitBytes
	}
	if cfg.ContextCompression.SummaryWindowMessages <= 0 {
		cfg.ContextCompression.SummaryWindowMessages = defaults.ContextCompression.SummaryWindowMessages
	}
	if cfg.ContextCompression.HardLimitBytes <= cfg.ContextCompression.SoftLimitBytes {
		cfg.ContextCompression.HardLimitBytes = defaults.ContextCompression.HardLimitBytes
		if cfg.ContextCompression.HardLimitBytes <= cfg.ContextCompression.SoftLimitBytes {
			cfg.ContextCompression.HardLimitBytes = cfg.ContextCompression.SoftLimitBytes + 1
		}
		warnings = append(warnings, "scanner_config.context_compression.hard_limit_bytes must be greater than soft_limit_bytes; falling back to a safe hard limit")
	}
	if cfg.ContextCompression.MicrocompactKeepRecent <= 0 {
		cfg.ContextCompression.MicrocompactKeepRecent = defaults.ContextCompression.MicrocompactKeepRecent
	}
	if cfg.ContextCompression.CompactMinTailMessages <= 0 {
		cfg.ContextCompression.CompactMinTailMessages = defaults.ContextCompression.CompactMinTailMessages
	}

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
