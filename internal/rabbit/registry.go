package rabbit

import (
	"fmt"
	"sync"

	"github.com/bancolombia/reactive-commons-go/internal/matcher"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// handlerRegistry is the internal implementation of async.HandlerRegistry.
// It stores handlers by message name and is safe for concurrent reads after Start().
//
// Event, command, and notification names may contain RabbitMQ topic-style
// wildcards ('*' single-segment, '#' multi-segment). Resolution at dispatch
// time is exact-match-first, falling back to the most specific matching
// pattern; see [matcher.MostSpecific] for the comparator. Query names cannot
// contain wildcards because they route through the direct exchange.
type handlerRegistry struct {
	mu            sync.RWMutex
	eventHandlers map[string]async.EventHandler[any]
	cmdHandlers   map[string]async.CommandHandler[any]
	queryHandlers map[string]async.QueryHandler[any, any]
	notifHandlers map[string]async.NotificationHandler[any]

	// Resolution caches: concrete delivered name -> matched pattern key, or
	// "" when no handler matches. Invalidated whenever a new handler is
	// registered. The maps are nil until first miss.
	eventCache map[string]string
	cmdCache   map[string]string
	notifCache map[string]string
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
	r.eventCache = nil
	return nil
}

func (r *handlerRegistry) ListenCommand(name string, h async.CommandHandler[any]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cmdHandlers[name]; exists {
		return fmt.Errorf("%w: command %q", async.ErrDuplicateHandler, name)
	}
	r.cmdHandlers[name] = h
	r.cmdCache = nil
	return nil
}

func (r *handlerRegistry) ServeQuery(name string, h async.QueryHandler[any, any]) error {
	if matcher.HasWildcard(name) {
		return fmt.Errorf("%w: query %q", async.ErrWildcardNotSupported, name)
	}
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
	r.notifCache = nil
	return nil
}

// EventHandler returns the registered handler for the given event name, or
// nil. Falls back to the most specific wildcard match when no exact key is
// registered. Resolutions are cached.
func (r *handlerRegistry) EventHandler(name string) async.EventHandler[any] {
	r.mu.RLock()
	if h, ok := r.eventHandlers[name]; ok {
		r.mu.RUnlock()
		return h
	}
	if pat, hit := r.eventCache[name]; hit {
		var h async.EventHandler[any]
		if pat != "" {
			h = r.eventHandlers[pat]
		}
		r.mu.RUnlock()
		return h
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check after upgrading the lock.
	if h, ok := r.eventHandlers[name]; ok {
		return h
	}
	if pat, hit := r.eventCache[name]; hit {
		if pat == "" {
			return nil
		}
		return r.eventHandlers[pat]
	}
	pat := resolvePattern(name, keysOf(r.eventHandlers))
	if r.eventCache == nil {
		r.eventCache = make(map[string]string)
	}
	r.eventCache[name] = pat
	if pat == "" {
		return nil
	}
	return r.eventHandlers[pat]
}

// CommandHandler returns the registered handler for the given command name,
// or nil. Falls back to the most specific wildcard match when no exact key
// is registered. Resolutions are cached.
func (r *handlerRegistry) CommandHandler(name string) async.CommandHandler[any] {
	r.mu.RLock()
	if h, ok := r.cmdHandlers[name]; ok {
		r.mu.RUnlock()
		return h
	}
	if pat, hit := r.cmdCache[name]; hit {
		var h async.CommandHandler[any]
		if pat != "" {
			h = r.cmdHandlers[pat]
		}
		r.mu.RUnlock()
		return h
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.cmdHandlers[name]; ok {
		return h
	}
	if pat, hit := r.cmdCache[name]; hit {
		if pat == "" {
			return nil
		}
		return r.cmdHandlers[pat]
	}
	pat := resolvePattern(name, keysOf(r.cmdHandlers))
	if r.cmdCache == nil {
		r.cmdCache = make(map[string]string)
	}
	r.cmdCache[name] = pat
	if pat == "" {
		return nil
	}
	return r.cmdHandlers[pat]
}

// QueryHandler returns the registered handler for the given query name, or nil.
// Wildcard resolution does not apply to queries.
func (r *handlerRegistry) QueryHandler(name string) async.QueryHandler[any, any] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.queryHandlers[name]
}

// NotificationHandler returns the registered handler for the given
// notification name, or nil. Falls back to the most specific wildcard match
// when no exact key is registered. Resolutions are cached.
func (r *handlerRegistry) NotificationHandler(name string) async.NotificationHandler[any] {
	r.mu.RLock()
	if h, ok := r.notifHandlers[name]; ok {
		r.mu.RUnlock()
		return h
	}
	if pat, hit := r.notifCache[name]; hit {
		var h async.NotificationHandler[any]
		if pat != "" {
			h = r.notifHandlers[pat]
		}
		r.mu.RUnlock()
		return h
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.notifHandlers[name]; ok {
		return h
	}
	if pat, hit := r.notifCache[name]; hit {
		if pat == "" {
			return nil
		}
		return r.notifHandlers[pat]
	}
	pat := resolvePattern(name, keysOf(r.notifHandlers))
	if r.notifCache == nil {
		r.notifCache = make(map[string]string)
	}
	r.notifCache[name] = pat
	if pat == "" {
		return nil
	}
	return r.notifHandlers[pat]
}

// resolvePattern returns the most specific wildcard pattern in keys that
// matches name, or "" when no pattern matches.
func resolvePattern(name string, keys []string) string {
	var matches []string
	for _, k := range keys {
		if matcher.Matches(name, k) {
			matches = append(matches, k)
		}
	}
	return matcher.MostSpecific(matches)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
