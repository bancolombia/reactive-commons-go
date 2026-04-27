package rabbit

import (
	"context"
	"testing"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func eventTagger(tag string) async.EventHandler[any] {
	return func(ctx context.Context, e async.DomainEvent[any]) error {
		_ = tag
		return nil
	}
}

func cmdTagger(tag string) async.CommandHandler[any] {
	return func(ctx context.Context, c async.Command[any]) error {
		_ = tag
		return nil
	}
}

func notifTagger(tag string) async.NotificationHandler[any] {
	return func(ctx context.Context, n async.Notification[any]) error {
		_ = tag
		return nil
	}
}

func TestRegistry_EventHandler_ExactMatchWinsOverWildcard(t *testing.T) {
	r := newHandlerRegistry()

	// Tag handlers by their behavior so the assertion can identify the winner.
	exactCalled := false
	wildcardCalled := false
	require.NoError(t, r.ListenEvent("order.created", func(ctx context.Context, e async.DomainEvent[any]) error {
		exactCalled = true
		return nil
	}))
	require.NoError(t, r.ListenEvent("order.*", func(ctx context.Context, e async.DomainEvent[any]) error {
		wildcardCalled = true
		return nil
	}))

	h := r.EventHandler("order.created")
	require.NotNil(t, h)
	_ = h(context.Background(), async.DomainEvent[any]{Name: "order.created"})

	assert.True(t, exactCalled, "exact handler must win when both are registered")
	assert.False(t, wildcardCalled, "wildcard handler must not fire when an exact match exists")
}

func TestRegistry_EventHandler_SingleWildcardMatch(t *testing.T) {
	r := newHandlerRegistry()
	called := false
	require.NoError(t, r.ListenEvent("order.*", func(ctx context.Context, e async.DomainEvent[any]) error {
		called = true
		return nil
	}))

	h := r.EventHandler("order.created")
	require.NotNil(t, h, "wildcard handler should resolve for matching key")
	_ = h(context.Background(), async.DomainEvent[any]{})
	assert.True(t, called)

	// '*' must not match across dot boundaries.
	assert.Nil(t, r.EventHandler("order.v2.created"))
}

func TestRegistry_EventHandler_HashWildcardMatch(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenEvent("order.#", eventTagger("order.#")))

	assert.NotNil(t, r.EventHandler("order"))
	assert.NotNil(t, r.EventHandler("order.created"))
	assert.NotNil(t, r.EventHandler("order.v2.created"))
	assert.Nil(t, r.EventHandler("invoice.created"))
}

func TestRegistry_EventHandler_PicksMostSpecificPattern(t *testing.T) {
	r := newHandlerRegistry()

	starCalled := false
	hashCalled := false
	require.NoError(t, r.ListenEvent("order.*", func(ctx context.Context, e async.DomainEvent[any]) error {
		starCalled = true
		return nil
	}))
	require.NoError(t, r.ListenEvent("order.#", func(ctx context.Context, e async.DomainEvent[any]) error {
		hashCalled = true
		return nil
	}))

	h := r.EventHandler("order.created")
	require.NotNil(t, h)
	_ = h(context.Background(), async.DomainEvent[any]{})
	assert.True(t, starCalled, "'*' is more specific than '#' and must win")
	assert.False(t, hashCalled)

	// Multi-segment: only '#' matches.
	starCalled, hashCalled = false, false
	h2 := r.EventHandler("order.v2.created")
	require.NotNil(t, h2)
	_ = h2(context.Background(), async.DomainEvent[any]{})
	assert.True(t, hashCalled)
	assert.False(t, starCalled)
}

func TestRegistry_EventHandler_NoMatchReturnsNil(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenEvent("order.*", eventTagger("order.*")))
	assert.Nil(t, r.EventHandler("invoice.created"))
}

func TestRegistry_EventHandler_CachesResolution(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenEvent("order.*", eventTagger("order.*")))

	// Trigger resolution and confirm the cache is populated.
	h := r.EventHandler("order.created")
	require.NotNil(t, h)

	r.mu.RLock()
	cachedPattern, hit := r.eventCache["order.created"]
	r.mu.RUnlock()
	assert.True(t, hit)
	assert.Equal(t, "order.*", cachedPattern)

	// Negative resolution is also cached.
	assert.Nil(t, r.EventHandler("invoice.x"))
	r.mu.RLock()
	cachedPattern, hit = r.eventCache["invoice.x"]
	r.mu.RUnlock()
	assert.True(t, hit)
	assert.Equal(t, "", cachedPattern)
}

func TestRegistry_EventHandler_RegistrationInvalidatesCache(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenEvent("order.#", eventTagger("order.#")))

	// Prime cache: only the broad pattern matches.
	require.NotNil(t, r.EventHandler("order.created"))
	r.mu.RLock()
	require.Equal(t, "order.#", r.eventCache["order.created"])
	r.mu.RUnlock()

	// Register a more specific pattern; cache must be dropped so the next
	// lookup re-resolves and picks the new winner.
	require.NoError(t, r.ListenEvent("order.*", eventTagger("order.*")))

	r.mu.RLock()
	assert.Nil(t, r.eventCache, "cache must be invalidated on registration")
	r.mu.RUnlock()

	require.NotNil(t, r.EventHandler("order.created"))
	r.mu.RLock()
	assert.Equal(t, "order.*", r.eventCache["order.created"])
	r.mu.RUnlock()
}

func TestRegistry_CommandHandler_WildcardResolution(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenCommand("invoice.*", cmdTagger("invoice.*")))

	assert.NotNil(t, r.CommandHandler("invoice.create"))
	assert.NotNil(t, r.CommandHandler("invoice.cancel"))
	assert.Nil(t, r.CommandHandler("invoice.v2.create"))
	assert.Nil(t, r.CommandHandler("payment.create"))
}

func TestRegistry_NotificationHandler_WildcardResolution(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenNotification("alerts.#", notifTagger("alerts.#")))

	assert.NotNil(t, r.NotificationHandler("alerts"))
	assert.NotNil(t, r.NotificationHandler("alerts.security.high"))
	assert.Nil(t, r.NotificationHandler("audits.security.high"))
}

func TestRegistry_QueryHandler_NoWildcardSupport(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ServeQuery("get-product", func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		return nil, nil
	}))

	// Wildcard lookup must not match the registered exact name, since
	// queries do not support wildcards on either side.
	assert.Nil(t, r.QueryHandler("get-product.v2"))
}
