package platform

import (
	"strings"

	"github.com/google/uuid"
)

// NewID returns a lowercase UUID suitable for task, event and usage rows.
func NewID() string {
	return strings.ToLower(uuid.NewString())
}

// IsValidID reports whether id looks like a non-empty UUID-ish identifier.
func IsValidID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
}
