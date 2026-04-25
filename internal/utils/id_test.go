package utils

import (
	"regexp"
	"testing"
)

var opaqueIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestNewOpaqueIDFormat(t *testing.T) {
	id := NewOpaqueID()
	if !opaqueIDPattern.MatchString(id) {
		t.Fatalf("expected opaque id to match %s, got %q", opaqueIDPattern.String(), id)
	}
}

func TestNewOpaqueIDUniqueUnderHighFrequency(t *testing.T) {
	const count = 10000

	seen := make(map[string]struct{}, count)
	for i := 0; i < count; i++ {
		id := NewOpaqueID()
		if !opaqueIDPattern.MatchString(id) {
			t.Fatalf("expected opaque id to match %s, got %q", opaqueIDPattern.String(), id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate opaque id generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}
