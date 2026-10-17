package rabbit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func restoreTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRestoreWithBackoff_RetriesUntilSuccess(t *testing.T) {
	log := restoreTestLogger()
	var gen uint64 = 1
	var attempts int
	err := errors.New("nope")

	done := make(chan struct{})
	go func() {
		restoreWithBackoff(context.Background(), 1, &gen, log, func() error {
			attempts++
			if attempts < 3 {
				return err
			}
			return nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("restoreWithBackoff did not finish")
	}
	assert.Equal(t, 3, attempts)
}

func TestRestoreWithBackoff_ExitsOnContextCancel(t *testing.T) {
	log := restoreTestLogger()
	var gen uint64 = 1
	err := errors.New("nope")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		restoreWithBackoff(ctx, 1, &gen, log, func() error {
			close(started)
			return err
		})
		close(done)
	}()

	<-started
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("restoreWithBackoff did not exit on context cancel")
	}
}

func TestRestoreWithBackoff_ExitsWhenSuperseded(t *testing.T) {
	log := restoreTestLogger()
	var gen uint64 = 1
	var attempts int
	err := errors.New("nope")

	done := make(chan struct{})
	go func() {
		restoreWithBackoff(context.Background(), 1, &gen, log, func() error {
			attempts++
			// A newer reconnect supersedes this loop during the first attempt.
			atomic.StoreUint64(&gen, 2)
			return err
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("restoreWithBackoff did not exit after being superseded")
	}
	assert.Equal(t, 1, attempts)
}
