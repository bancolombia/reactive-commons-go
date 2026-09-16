package kafka

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestRetryBackoff verifies the exponential retry schedule: delay doubles per
// attempt, starting from RetryInitialDelay, capped at RetryMaxDelay.
func TestRetryBackoff(t *testing.T) {
	t.Parallel()

	initial := 1 * time.Second
	maxDelay := 30 * time.Second

	tests := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{"attempt 0 -> initial", 0, 1 * time.Second},
		{"attempt 1 -> initial*2", 1, 2 * time.Second},
		{"attempt 2 -> initial*4", 2, 4 * time.Second},
		{"attempt 3 -> initial*8", 3, 8 * time.Second},
		{"attempt 4 -> initial*16", 4, 16 * time.Second},
		{"attempt 5 -> capped at max (would be 32s)", 5, 30 * time.Second},
		{"attempt 10 -> capped at max", 10, 30 * time.Second},
		{"negative attempt clamped to 0", -1, 1 * time.Second},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := retryBackoff(tc.attempt, initial, maxDelay)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRetryBackoff_DifferentBaseline exercises smaller values used by
// integration tests to prove the shape holds regardless of the initial delay.
func TestRetryBackoff_DifferentBaseline(t *testing.T) {
	t.Parallel()

	initial := 50 * time.Millisecond
	maxDelay := 200 * time.Millisecond

	assert.Equal(t, 50*time.Millisecond, retryBackoff(0, initial, maxDelay))
	assert.Equal(t, 100*time.Millisecond, retryBackoff(1, initial, maxDelay))
	assert.Equal(t, 200*time.Millisecond, retryBackoff(2, initial, maxDelay))
	assert.Equal(t, 200*time.Millisecond, retryBackoff(3, initial, maxDelay))
	assert.Equal(t, 200*time.Millisecond, retryBackoff(100, initial, maxDelay))
}
