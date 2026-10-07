package kafka

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTopicAndGroupIDDerivation(t *testing.T) {
	t.Parallel()

	base := Config{
		AppName:             "svc",
		ConsumerGroupPrefix: "svc",
	}

	tests := []struct {
		name     string
		cfg      Config
		fn       func(Config) string
		wantOut  string
		instance string
	}{
		{
			name:    "event topic uses AppName prefix by default",
			cfg:     base,
			fn:      func(c Config) string { return topicForEvent(c, "user.created") },
			wantOut: "svc.user.created",
		},
		{
			name:    "notification topic uses AppName prefix by default",
			cfg:     base,
			fn:      func(c Config) string { return topicForNotification(c, "cache.invalidated") },
			wantOut: "svc.cache.invalidated",
		},
		{
			name: "TopicNameFunc override",
			cfg: Config{
				AppName:       "svc",
				TopicNameFunc: func(n string) string { return "org." + n },
			},
			fn:      func(c Config) string { return topicForEvent(c, "user.created") },
			wantOut: "org.user.created",
		},
		{
			name:    "event group ID includes prefix + name",
			cfg:     base,
			fn:      func(c Config) string { return groupIDForEvent(c, "user.created") },
			wantOut: "svc.user.created",
		},
		{
			name:    "notification group ID includes instance",
			cfg:     base,
			fn:      func(c Config) string { return groupIDForNotification(c, "cache.invalidated", "i-1") },
			wantOut: "svc.cache.invalidated.i-1",
		},
		{
			name: "group ID falls back to AppName when prefix empty",
			cfg: Config{
				AppName: "svc",
			},
			fn:      func(c Config) string { return groupIDForEvent(c, "user.created") },
			wantOut: "svc.user.created",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantOut, tc.fn(tc.cfg))
		})
	}
}
