package unit_test

import (
	"strings"
	"testing"

	"github.com/bancolombia/reactive-commons-go/internal/utils"
	"github.com/stretchr/testify/assert"
)

func TestFromNameWithSuffix(t *testing.T) {
	assert.Equal(t, "svc.subsEvents", utils.FromNameWithSuffix("svc", "subsEvents"))
	assert.Equal(t, "order-service.query", utils.FromNameWithSuffix("order-service", "query"))
	assert.Equal(t, "my-app.replies", utils.FromNameWithSuffix("my-app", "replies"))
}

func TestGenerateTempName_Format(t *testing.T) {
	name := utils.GenerateTempName("order-service", "replies")
	// Expected format: "order-service.replies.<base64uuid>"
	assert.True(t, strings.HasPrefix(name, "order-service.replies."),
		"expected prefix 'order-service.replies.' got %q", name)

	parts := strings.SplitN(name, ".", 3)
	assert.Len(t, parts, 3, "expected 3 dot-separated parts")

	suffix := parts[2]
	assert.NotEmpty(t, suffix, "base64 UUID suffix must not be empty")
	// Must not contain padding '='
	assert.NotContains(t, suffix, "=", "base64 suffix must not contain padding")
}

func TestGenerateTempName_IsUnique(t *testing.T) {
	n1 := utils.GenerateTempName("app", "notification")
	n2 := utils.GenerateTempName("app", "notification")
	assert.NotEqual(t, n1, n2, "two temp names must be unique")
}

func TestGenerateTempName_URLSafeBase64(t *testing.T) {
	// URL-safe base64 uses '-' and '_' instead of '+' and '/'
	for i := 0; i < 20; i++ {
		name := utils.GenerateTempName("app", "replies")
		parts := strings.SplitN(name, ".", 3)
		suffix := parts[2]
		assert.NotContains(t, suffix, "+", "base64 suffix must be URL-safe (no '+')")
		assert.NotContains(t, suffix, "/", "base64 suffix must be URL-safe (no '/')")
	}
}
