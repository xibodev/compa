package utils

import (
	"fmt"
	"strings"
)

// ValidateSkillIdentifier validates that the given skill identifier (slug,
// registry name or install folder name) is one plain name: not empty, not ".",
// and without path separators ("/", "\\") or "..".
func ValidateSkillIdentifier(identifier string) error {
	trimmed := strings.TrimSpace(identifier)
	if trimmed == "" {
		return fmt.Errorf("identifier is required and must be a non-empty string")
	}
	if trimmed == "." || strings.ContainsAny(trimmed, "/\\") || strings.Contains(trimmed, "..") {
		return fmt.Errorf("identifier must not be '.' or contain path separators or '..' to prevent directory traversal")
	}
	return nil
}
