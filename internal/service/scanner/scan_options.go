package scanner

import (
	"strings"

	"codescan/internal/config"
)

type ScanExecutionProfile struct {
	Model string
}

func (p ScanExecutionProfile) modelOrDefault() string {
	if model := strings.TrimSpace(p.Model); model != "" {
		return model
	}
	return strings.TrimSpace(config.AI.Model)
}

type ScanExecutionOptions struct {
	ManageTaskStatus  bool
	PersistTaskRecord bool
	Profile           ScanExecutionProfile
}

func legacyScanExecutionOptions() ScanExecutionOptions {
	return ScanExecutionOptions{
		ManageTaskStatus:  true,
		PersistTaskRecord: true,
	}
}

func orchestratedInitExecutionOptions() ScanExecutionOptions {
	return ScanExecutionOptions{
		ManageTaskStatus:  false,
		PersistTaskRecord: true,
	}
}

func orchestratedStageExecutionOptions() ScanExecutionOptions {
	return ScanExecutionOptions{
		ManageTaskStatus:  false,
		PersistTaskRecord: false,
	}
}
