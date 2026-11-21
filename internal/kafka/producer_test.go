package kafka

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	kgo "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"
)

// fakeWriter fails the first n writes with err and succeeds afterwards.
type fakeWriter struct {
	failures int
	err      error
	writes   int
}

func (f *fakeWriter) WriteMessages(_ context.Context, _ ...kgo.Message) error {
	f.writes++
	if f.failures > 0 {
		f.failures--
		return f.err
	}
	return nil
}

func (f *fakeWriter) Close() error { return nil }

func TestPublish_RetriesTransientTopicErrors(t *testing.T) {
	w := &fakeWriter{failures: 2, err: kgo.UnknownTopicOrPartition}
	cfg := Config{MaxRetryAttempts: 5, RetryInitialDelay: time.Millisecond, RetryMaxDelay: time.Millisecond}
	p := &producer{w: w, cfg: cfg}

	require.NoError(t, p.publish(context.Background(), "t", nil, nil, nil))
	require.Equal(t, 3, w.writes, "publish should retry transient topic errors")
}

func TestPublish_GivesUpAfterMaxAttempts(t *testing.T) {
	w := &fakeWriter{failures: 100, err: kgo.UnknownTopicOrPartition}
	cfg := Config{MaxRetryAttempts: 3, RetryInitialDelay: time.Millisecond, RetryMaxDelay: time.Millisecond}
	p := &producer{w: w, cfg: cfg}

	err := p.publish(context.Background(), "t", nil, nil, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, kgo.UnknownTopicOrPartition)
	require.Equal(t, 3, w.writes)
}

func TestPublish_NoRetryOnPermanentErrors(t *testing.T) {
	w := &fakeWriter{failures: 100, err: errors.New("boom")}
	cfg := Config{MaxRetryAttempts: 5, RetryInitialDelay: time.Millisecond, RetryMaxDelay: time.Millisecond}
	p := &producer{w: w, cfg: cfg}

	err := p.publish(context.Background(), "t", nil, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
	require.Equal(t, 1, w.writes)
}

func TestPublish_StopsRetryingWhenContextDone(t *testing.T) {
	w := &fakeWriter{failures: 100, err: kgo.UnknownTopicOrPartition}
	cfg := Config{MaxRetryAttempts: 100, RetryInitialDelay: time.Hour, RetryMaxDelay: time.Hour}
	p := &producer{w: w, cfg: cfg}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := p.publish(ctx, "t", nil, nil, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, kgo.UnknownTopicOrPartition)
	require.Less(t, time.Since(start), time.Second, "retry loop should stop when ctx is done")
}

func TestIsTransientTopicError(t *testing.T) {
	require.True(t, isTransientTopicError(kgo.UnknownTopicOrPartition))
	require.True(t, isTransientTopicError(fmt.Errorf("wrapped: %w", kgo.UnknownTopicOrPartition)))
	require.True(t, isTransientTopicError(kgo.LeaderNotAvailable))
	require.False(t, isTransientTopicError(kgo.NotLeaderForPartition))
	require.False(t, isTransientTopicError(context.Canceled))
	require.False(t, isTransientTopicError(errors.New("boom")))
	require.False(t, isTransientTopicError(nil))
}
