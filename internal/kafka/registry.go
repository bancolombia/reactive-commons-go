package kafka

import (
	"fmt"
	"sync"

	"github.com/bancolombia/reactive-commons-go/internal/matcher"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// handlerRegistry implements async.HandlerRegistry. Registration is only
// allowed before Start; markStarted closes the registry.
//
// Event and notification handlers are consumed as one topic per registered
// name, so no wildcard resolution happens for them. Command handlers share a
// single topic per app and are dispatched by name at consume time, so command
// names may contain wildcards ('*' single-segment, '#' multi-segment) and are
// resolved exact-match-first, falling back to the most specific matching
// pattern (mirrors the rabbit transport). Query names cannot contain
// wildcards: they are exact-match only.
type handlerRegistry struct {
	mu                   sync.RWMutex
	started              bool
	eventHandlers        map[string]async.EventHandler[any]
	notificationHandlers map[string]async.NotificationHandler[any]
	commandHandlers      map[string]async.CommandHandler[any]
	queryHandlers        map[string]async.QueryHandler[any, any]
}

var _ async.HandlerRegistry = (*handlerRegistry)(nil)

func newHandlerRegistry() *handlerRegistry {
	return &handlerRegistry{
		eventHandlers:        map[string]async.EventHandler[any]{},
		notificationHandlers: map[string]async.NotificationHandler[any]{},
		commandHandlers:      map[string]async.CommandHandler[any]{},
		queryHandlers:        map[string]async.QueryHandler[any, any]{},
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
		return fmt.Errorf("%w: event %q", async.ErrDuplicateHandler, eventName)
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
		return fmt.Errorf("%w: notification %q", async.ErrDuplicateHandler, name)
	}
	r.notificationHandlers[name] = handler
	return nil
}

func (r *handlerRegistry) ListenCommand(name string, handler async.CommandHandler[any]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return async.ErrRegistrationClosed
	}
	if _, dup := r.commandHandlers[name]; dup {
		return fmt.Errorf("%w: command %q", async.ErrDuplicateHandler, name)
	}
	r.commandHandlers[name] = handler
	return nil
}

func (r *handlerRegistry) ServeQuery(name string, handler async.QueryHandler[any, any]) error {
	if matcher.HasWildcard(name) {
		return fmt.Errorf("%w: query %q", async.ErrWildcardNotSupported, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return async.ErrRegistrationClosed
	}
	if _, dup := r.queryHandlers[name]; dup {
		return fmt.Errorf("%w: query %q", async.ErrDuplicateHandler, name)
	}
	r.queryHandlers[name] = handler
	return nil
}

// CommandHandler returns the registered handler for the given command name,
// or nil. Falls back to the most specific wildcard match when no exact key is
// registered.
func (r *handlerRegistry) CommandHandler(name string) async.CommandHandler[any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if h, ok := r.commandHandlers[name]; ok {
		return h
	}
	pat := matcher.Resolve(name, keysOf(r.commandHandlers))
	if pat == "" {
		return nil
	}
	return r.commandHandlers[pat]
}

// QueryHandler returns the registered handler for the given query resource,
// or nil. Wildcard resolution does not apply to queries.
func (r *handlerRegistry) QueryHandler(name string) async.QueryHandler[any, any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.queryHandlers[name]
}

// snapshotEventHandlers returns a copy of the registered event handlers.
// Called by KafkaApp.Start after markStarted.
func (r *handlerRegistry) snapshotEventHandlers() map[string]async.EventHandler[any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]async.EventHandler[any], len(r.eventHandlers))
	for k, v := range r.eventHandlers {
		out[k] = v
	}
	return out
}

// snapshotNotificationHandlers returns a copy of the registered notification
// handlers.
func (r *handlerRegistry) snapshotNotificationHandlers() map[string]async.NotificationHandler[any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]async.NotificationHandler[any], len(r.notificationHandlers))
	for k, v := range r.notificationHandlers {
		out[k] = v
	}
	return out
}

// snapshotCommandHandlers returns a copy of the registered command handlers.
func (r *handlerRegistry) snapshotCommandHandlers() map[string]async.CommandHandler[any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]async.CommandHandler[any], len(r.commandHandlers))
	for k, v := range r.commandHandlers {
		out[k] = v
	}
	return out
}

// snapshotQueryHandlers returns a copy of the registered query handlers.
func (r *handlerRegistry) snapshotQueryHandlers() map[string]async.QueryHandler[any, any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]async.QueryHandler[any, any], len(r.queryHandlers))
	for k, v := range r.queryHandlers {
		out[k] = v
	}
	return out
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
