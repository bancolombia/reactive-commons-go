package rabbit

import (
	"fmt"
	"sync"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// handlerRegistry is the internal implementation of async.HandlerRegistry.
// It stores handlers by message name and is safe for concurrent reads after Start().
type handlerRegistry struct {
	mu            sync.RWMutex
	eventHandlers map[string]async.EventHandler[any]
	cmdHandlers   map[string]async.CommandHandler[any]
	queryHandlers map[string]async.QueryHandler[any, any]
	notifHandlers map[string]async.NotificationHandler[any]
}

func newHandlerRegistry() *handlerRegistry {
	return &handlerRegistry{
		eventHandlers: make(map[string]async.EventHandler[any]),
		cmdHandlers:   make(map[string]async.CommandHandler[any]),
		queryHandlers: make(map[string]async.QueryHandler[any, any]),
		notifHandlers: make(map[string]async.NotificationHandler[any]),
	}
}

func (r *handlerRegistry) ListenEvent(name string, h async.EventHandler[any]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.eventHandlers[name]; exists {
		return fmt.Errorf("%w: event %q", async.ErrDuplicateHandler, name)
	}
	r.eventHandlers[name] = h
	return nil
}

func (r *handlerRegistry) ListenCommand(name string, h async.CommandHandler[any]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cmdHandlers[name]; exists {
		return fmt.Errorf("%w: command %q", async.ErrDuplicateHandler, name)
	}
	r.cmdHandlers[name] = h
	return nil
}

func (r *handlerRegistry) ServeQuery(name string, h async.QueryHandler[any, any]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.queryHandlers[name]; exists {
		return fmt.Errorf("%w: query %q", async.ErrDuplicateHandler, name)
	}
	r.queryHandlers[name] = h
	return nil
}

func (r *handlerRegistry) ListenNotification(name string, h async.NotificationHandler[any]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.notifHandlers[name]; exists {
		return fmt.Errorf("%w: notification %q", async.ErrDuplicateHandler, name)
	}
	r.notifHandlers[name] = h
	return nil
}

// EventHandler returns the registered handler for the given event name, or nil.
func (r *handlerRegistry) EventHandler(name string) async.EventHandler[any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.eventHandlers[name]
}

// CommandHandler returns the registered handler for the given command name, or nil.
func (r *handlerRegistry) CommandHandler(name string) async.CommandHandler[any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cmdHandlers[name]
}

// QueryHandler returns the registered handler for the given query name, or nil.
func (r *handlerRegistry) QueryHandler(name string) async.QueryHandler[any, any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.queryHandlers[name]
}

// NotificationHandler returns the registered handler for the given notification name, or nil.
func (r *handlerRegistry) NotificationHandler(name string) async.NotificationHandler[any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.notifHandlers[name]
}

// EventNames returns all registered event handler names.
func (r *handlerRegistry) EventNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.eventHandlers))
	for n := range r.eventHandlers {
		names = append(names, n)
	}
	return names
}

// CommandNames returns all registered command handler names.
func (r *handlerRegistry) CommandNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.cmdHandlers))
	for n := range r.cmdHandlers {
		names = append(names, n)
	}
	return names
}

// QueryNames returns all registered query handler names.
func (r *handlerRegistry) QueryNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.queryHandlers))
	for n := range r.queryHandlers {
		names = append(names, n)
	}
	return names
}

// NotificationNames returns all registered notification handler names.
func (r *handlerRegistry) NotificationNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.notifHandlers))
	for n := range r.notifHandlers {
		names = append(names, n)
	}
	return names
}
