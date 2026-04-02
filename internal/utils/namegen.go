// Package utils provides internal utilities for reactive-commons-go.
package utils

import (
	"encoding/base64"
	"strings"

	"github.com/google/uuid"
)

// FromNameWithSuffix constructs a queue name by joining appName and suffix with a dot.
// Mirrors Java NameGenerator.fromNameWithSuffix(appName, suffix).
// Example: FromNameWithSuffix("order-service", "subsEvents") → "order-service.subsEvents"
func FromNameWithSuffix(appName, suffix string) string {
	return appName + "." + suffix
}

// GenerateTempName creates a unique per-startup temporary queue name.
// Mirrors Java NameGenerator.generateTempName(appName, suffix) which appends a
// URL-safe base64-encoded UUID (no padding).
// Example: GenerateTempName("order-service", "replies") → "order-service.replies.<base64uuid>"
func GenerateTempName(appName, suffix string) string {
	id := uuid.New()
	b64 := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(id[:])
	// Strip any trailing '=' padding just in case
	b64 = strings.TrimRight(b64, "=")
	return appName + "." + suffix + "." + b64
}
