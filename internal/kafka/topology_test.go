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
		{
			name:    "command topic targets the given app",
			cfg:     base,
			fn:      func(c Config) string { return topicForCommand(c, "remote") },
			wantOut: "remote.commands",
		},
		{
			name:    "query topic targets the given app",
			cfg:     base,
			fn:      func(c Config) string { return topicForQuery(c, "remote") },
			wantOut: "remote.queries",
		},
		{
			name:    "reply topic uses own AppName",
			cfg:     base,
			fn:      func(c Config) string { return topicForReply(c) },
			wantOut: "svc.replies",
		},
		{
			name: "command/query/reply topic func overrides",
			cfg: Config{
				AppName:               "svc",
				CommandsTopicNameFunc: func(app string) string { return "cmds." + app },
				QueriesTopicNameFunc:  func(app string) string { return "qs." + app },
				RepliesTopicNameFunc:  func(app string) string { return "replies." + app },
			},
			fn: func(c Config) string {
				return topicForCommand(c, "remote") + "|" + topicForQuery(c, "remote") + "|" + topicForReply(c)
			},
			wantOut: "cmds.remote|qs.remote|replies.svc",
		},
		{
			name:    "command group ID",
			cfg:     base,
			fn:      func(c Config) string { return groupIDForCommand(c) },
			wantOut: "svc.commands",
		},
		{
			name:    "query group ID",
			cfg:     base,
			fn:      func(c Config) string { return groupIDForQuery(c) },
			wantOut: "svc.queries",
		},
		{
			name: "reply group ID includes instance",
			cfg: Config{
				AppName:             "svc",
				ConsumerGroupPrefix: "svc",
				InstanceID:          "i-9",
			},
			fn:      func(c Config) string { return groupIDForReply(c) },
			wantOut: "svc.replies.i-9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantOut, tc.fn(tc.cfg))
		})
	}
}
