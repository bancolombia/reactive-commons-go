package kafka

import (
	"context"
	"errors"
	"log/slog"
	"time"

	kgo "github.com/segmentio/kafka-go"
)

// consumerLoop is the shared fetch/dispatch skeleton used by every kafka
// listener (command, query, reply, event, notification). Callers inject the
// fetch function (FetchMessage for manual-commit flows, ReadMessage for
// auto-commit flows) and the per-message handler; the loop owns the retry
// backoff and shutdown semantics.
type consumerLoop struct {
	kind   string
	topic  string
	logger *slog.Logger
	fetch  func(context.Context) (kgo.Message, error)
	handle func(context.Context, kgo.Message)
}

func (c consumerLoop) run(ctx context.Context) {
	const fetchBackoff = 1 * time.Second
	for {
		msg, err := c.fetch(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return
			}
			c.logger.Warn("kafka: "+c.kind+" fetch failed; retrying",
				"topic", c.topic, "err", err, "backoff", fetchBackoff)
			select {
			case <-time.After(fetchBackoff):
			case <-ctx.Done():
				return
			}
			continue
		}
		c.handle(ctx, msg)
	}
}
