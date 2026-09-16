package kafka

import (
	"fmt"
	"sync"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// handlerRegistry implements async.HandlerRegistry. Only ListenEvent and
// ListenNotification do real work; ListenCommand and ServeQuery return
// kafka.ErrNotSupportedOnKafka.
type handlerRegistry struct {
	mu                   sync.Mutex
	started              bool
	eventHandlers        map[string]async.EventHandler[any]
	notificationHandlers map[string]async.NotificationHandler[any]
}

var _ async.HandlerRegistry = (*handlerRegistry)(nil)

func newHandlerRegistry() *handlerRegistry {
	return &handlerRegistry{
		eventHandlers:        map[string]async.EventHandler[any]{},
		notificationHandlers: map[string]async.NotificationHandler[any]{},
	}
}

func (r *handlerRegistry) markStarted() {
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
}

func (r *handlerRegistry) ListenEvent(eventName string, handler async.EventHandler[any]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return async.ErrRegistrationClosed
	}
	if _, dup := r.eventHandlers[eventName]; dup {
		return async.ErrDuplicateHandler
	}
	r.eventHandlers[eventName] = handler
	return nil
}

func (r *handlerRegistry) ListenNotification(name string, handler async.NotificationHandler[any]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return async.ErrRegistrationClosed
	}
	if _, dup := r.notificationHandlers[name]; dup {
		return async.ErrDuplicateHandler
	}
	r.notificationHandlers[name] = handler
	return nil
}

func (r *handlerRegistry) ListenCommand(_ string, _ async.CommandHandler[any]) error {
	return fmt.Errorf("kafka: %s: %w", "ListenCommand", ErrNotSupportedOnKafka)
}

func (r *handlerRegistry) ServeQuery(_ string, _ async.QueryHandler[any, any]) error {
	return fmt.Errorf("kafka: %s: %w", "ServeQuery", ErrNotSupportedOnKafka)
}

// snapshotEventHandlers returns a copy of the registered event handlers.
// Called by KafkaApp.Start after markStarted.
func (r *handlerRegistry) snapshotEventHandlers() map[string]async.EventHandler[any] {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]async.EventHandler[any], len(r.eventHandlers))
	for k, v := range r.eventHandlers {
		out[k] = v
	}
	return out
}

// snapshotNotificationHandlers returns a copy of the registered notification
// handlers.
func (r *handlerRegistry) snapshotNotificationHandlers() map[string]async.NotificationHandler[any] {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]async.NotificationHandler[any], len(r.notificationHandlers))
	for k, v := range r.notificationHandlers {
		out[k] = v
	}
	return out
}
