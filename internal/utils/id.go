package utils

import (
	"strings"

	"github.com/google/uuid"
)

// NewOpaqueID returns a lowercase UUID string without hyphens for entity IDs.
func NewOpaqueID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}
