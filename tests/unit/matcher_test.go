package unit_test

import (
	"testing"

	"github.com/bancolombia/reactive-commons-go/internal/matcher"
	"github.com/stretchr/testify/assert"
)

func TestMatcher_HasWildcard(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"order.created", false},
		{"order.*", true},
		{"order.#", true},
		{"#", true},
		{"*", true},
		{"", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, matcher.HasWildcard(tc.in), tc.in)
	}
}

func TestMatcher_Matches(t *testing.T) {
	cases := []struct {
		pattern    string
		routingKey string
		want       bool
	}{
		// '*' matches exactly one segment.
		{"order.*", "order.created", true},
		{"order.*", "order.v2.created", false},
		{"order.*", "order", false},
		{"*.created", "order.created", true},
		{"*.created", "order.v2.created", false},

		// '#' matches zero or more segments.
		{"order.#", "order", true},
		{"order.#", "order.created", true},
		{"order.#", "order.v2.created", true},
		{"#", "anything.at.all", true},
		{"#", "", true},

		// Mixed.
		{"order.*.completed", "order.42.completed", true},
		{"order.*.completed", "order.completed", false},
		// '#' can match zero segments, so the dots collapse.
		{"order.#.completed", "order.completed", true},
		{"order.#.completed", "order.42.completed", true},
		{"order.#.completed", "order.42.v2.completed", true},
		{"order.#.completed", "order.42.failed", false},

		// No wildcard => never matches via Matches (callers do exact check).
		{"order.created", "order.created", false},
		{"order.created", "order.updated", false},
	}
	for _, tc := range cases {
		got := matcher.Matches(tc.routingKey, tc.pattern)
		assert.Equal(t, tc.want, got, "pattern=%q key=%q", tc.pattern, tc.routingKey)
	}
}

func TestMatcher_MostSpecific_Empty(t *testing.T) {
	assert.Equal(t, "", matcher.MostSpecific(nil))
	assert.Equal(t, "", matcher.MostSpecific([]string{}))
}

func TestMatcher_MostSpecific_SingleWildcardBeatsHash(t *testing.T) {
	got := matcher.MostSpecific([]string{"foo.#", "foo.*"})
	assert.Equal(t, "foo.*", got)
}

func TestMatcher_MostSpecific_FewerWildcardsWin(t *testing.T) {
	// "order.*.completed" has one wildcard; "order.#" has one but
	// hash is less specific.
	got := matcher.MostSpecific([]string{"order.#", "order.*.completed"})
	assert.Equal(t, "order.*.completed", got)
}

func TestMatcher_MostSpecific_LongerWins(t *testing.T) {
	// Both are pure '#' wildcards but the longer expression is more specific.
	got := matcher.MostSpecific([]string{"#", "order.#"})
	assert.Equal(t, "order.#", got)
}

func TestMatcher_MostSpecific_Single(t *testing.T) {
	assert.Equal(t, "only.one", matcher.MostSpecific([]string{"only.one"}))
}
