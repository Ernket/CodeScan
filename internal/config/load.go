package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type ConfigFieldError struct {
	Path        string
	Removed     bool
	Replacement string
}

func (e *ConfigFieldError) Error() string {
	if e.Removed {
		if e.Replacement != "" {
			return fmt.Sprintf("removed config field %q; %s", e.Path, e.Replacement)
		}
		return fmt.Sprintf("removed config field %q; delete it from the config file", e.Path)
	}
	return fmt.Sprintf("unknown config field %q", e.Path)
}

type configSchemaNode struct {
	children map[string]configSchemaNode
	removed  map[string]string
}

func LoadConfigFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return DecodeConfigJSON(data)
}

func DecodeConfigJSON(data []byte) (Config, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return Config{}, fmt.Errorf("config file is empty")
	}
	if err := validateConfigKeys(trimmed); err != nil {
		return Config{}, err
	}

	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config JSON: %w", err)
	}
	if err := ensureSingleJSONObject(decoder); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func ensureSingleJSONObject(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return fmt.Errorf("invalid config JSON: %w", err)
		}
		return fmt.Errorf("config file must contain a single JSON object")
	}
	return nil
}

func validateConfigKeys(data []byte) error {
	var rootAny any
	if err := json.Unmarshal(data, &rootAny); err != nil {
		return fmt.Errorf("invalid config JSON: %w", err)
	}
	if _, ok := rootAny.(map[string]any); !ok {
		return fmt.Errorf("config file must contain a JSON object")
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("invalid config JSON: %w", err)
	}
	return validateConfigObject(configSchema(), "", root)
}

func validateConfigObject(node configSchemaNode, prefix string, object map[string]json.RawMessage) error {
	for key, value := range object {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		if replacement, ok := node.removed[key]; ok {
			return &ConfigFieldError{
				Path:        path,
				Removed:     true,
				Replacement: replacement,
			}
		}

		child, ok := node.children[key]
		if !ok {
			return &ConfigFieldError{Path: path}
		}
		if len(child.children) == 0 && len(child.removed) == 0 {
			continue
		}

		var nested map[string]json.RawMessage
		if err := json.Unmarshal(value, &nested); err != nil {
			continue
		}
		if err := validateConfigObject(child, path, nested); err != nil {
			return err
		}
	}
	return nil
}

func configSchema() configSchemaNode {
	return configSchemaNode{
		children: map[string]configSchemaNode{
			"auth_key": leafSchema(),
			"db_config": {
				children: map[string]configSchemaNode{
					"host":     leafSchema(),
					"port":     leafSchema(),
					"user":     leafSchema(),
					"password": leafSchema(),
					"dbname":   leafSchema(),
				},
			},
			"ai_config": {
				children: map[string]configSchemaNode{
					"api_key":  leafSchema(),
					"base_url": leafSchema(),
					"model":    leafSchema(),
				},
			},
			"scanner_config": {
				children: map[string]configSchemaNode{
					"context_compression": {
						children: map[string]configSchemaNode{
							"soft_limit_tokens": leafSchema(),
							"hard_limit_tokens": leafSchema(),
						},
						removed: map[string]string{
							"soft_limit_bytes":          "delete it from the config file; byte fallback threshold is now built in",
							"hard_limit_bytes":          "delete it from the config file; byte fallback threshold is now built in",
							"summary_window_messages":   "delete it from the config file; summary window tuning is now built in",
							"microcompact_keep_recent":  "delete it from the config file; micro-compaction tuning is now built in",
							"compact_min_tail_messages": "delete it from the config file; micro-compaction tuning is now built in",
							"session_memory_enabled":    "delete it from the config file; it now follows scanner_config.session_memory.enabled",
						},
					},
					"session_memory": {
						children: map[string]configSchemaNode{
							"enabled": leafSchema(),
						},
						removed: map[string]string{
							"min_growth_bytes":         "delete it from the config file; this threshold is now built in",
							"min_tool_calls":           "delete it from the config file; this threshold is now built in",
							"max_update_bytes":         "delete it from the config file; this threshold is now built in",
							"failure_cooldown_seconds": "delete it from the config file; this threshold is now built in",
							"request_timeout_seconds":  "delete it from the config file",
							"max_retries":              "delete it from the config file",
							"retry_backoff_seconds":    "delete it from the config file",
						},
					},
				},
				removed: map[string]string{
					"context_soft_limit_bytes":        "delete it from the config file; context limits are now built in",
					"context_hard_limit_bytes":        "delete it from the config file; context limits are now built in",
					"context_summary_window_messages": "delete it from the config file; summary window tuning is now built in",
				},
			},
			"orchestration_config": {
				children: map[string]configSchemaNode{
					"enabled": leafSchema(),
					"worker": {
						children: map[string]configSchemaNode{
							"model": leafSchema(),
						},
						removed: map[string]string{
							"parallelism":    "delete it from the config file; worker fan-out is now built in",
							"temperature":    "delete it from the config file",
							"max_iterations": "delete it from the config file",
						},
					},
					"validator": {
						children: map[string]configSchemaNode{
							"model": leafSchema(),
						},
						removed: map[string]string{
							"parallelism":    "delete it from the config file; validator fan-out is now built in",
							"temperature":    "delete it from the config file",
							"max_iterations": "delete it from the config file",
						},
					},
				},
				removed: map[string]string{
					"sse_heartbeat_seconds": "delete it from the config file; heartbeat tuning is now built in",
					"planner":               "delete it from the config file; planner runtime settings are now built in",
					"integrator":            "delete it from the config file; integrator runtime settings are now built in",
					"persistence":           "delete it from the config file; persistence runtime settings are now built in",
					"prompt_source":         "delete it from the config file",
				},
			},
		},
	}
}

func leafSchema() configSchemaNode {
	return configSchemaNode{}
}
