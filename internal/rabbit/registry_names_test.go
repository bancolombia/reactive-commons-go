package rabbit

import (
	"context"
	"sort"
	"testing"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_EventNames_ReturnsRegistered(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenEvent("a", eventTagger("a")))
	require.NoError(t, r.ListenEvent("b", eventTagger("b")))

	got := r.EventNames()
	sort.Strings(got)
	assert.Equal(t, []string{"a", "b"}, got)
}

func TestRegistry_CommandNames_ReturnsRegistered(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenCommand("c1", cmdTagger("c1")))
	require.NoError(t, r.ListenCommand("c2", cmdTagger("c2")))

	got := r.CommandNames()
	sort.Strings(got)
	assert.Equal(t, []string{"c1", "c2"}, got)
}

func TestRegistry_QueryNames_ReturnsRegistered(t *testing.T) {
	r := newHandlerRegistry()
	q := func(ctx context.Context, query async.AsyncQuery[any], from async.From) (any, error) {
		return nil, nil
	}
	require.NoError(t, r.ServeQuery("q1", q))
	require.NoError(t, r.ServeQuery("q2", q))

	got := r.QueryNames()
	sort.Strings(got)
	assert.Equal(t, []string{"q1", "q2"}, got)
}

func TestRegistry_NotificationNames_ReturnsRegistered(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenNotification("n1", notifTagger("n1")))
	require.NoError(t, r.ListenNotification("n2", notifTagger("n2")))

	got := r.NotificationNames()
	sort.Strings(got)
	assert.Equal(t, []string{"n1", "n2"}, got)
}

func TestRegistry_Names_EmptyByDefault(t *testing.T) {
	r := newHandlerRegistry()
	assert.Empty(t, r.EventNames())
	assert.Empty(t, r.CommandNames())
	assert.Empty(t, r.QueryNames())
	assert.Empty(t, r.NotificationNames())
}

func TestRegistry_DuplicateNotification_ReturnsError(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenNotification("alerts", notifTagger("alerts")))
	err := r.ListenNotification("alerts", notifTagger("alerts"))
	assert.ErrorIs(t, err, async.ErrDuplicateHandler)
}

// Re-invoking the resolver after the first call exercises the read-lock cache
// hit path that returns without acquiring the write lock — both for positive
// and negative resolutions.
func TestRegistry_EventHandler_ReadLockCacheHit_Positive(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenEvent("order.*", eventTagger("order.*")))

	// First call populates the cache through the write-lock path.
	require.NotNil(t, r.EventHandler("order.created"))
	// Second call should be served from the cache via the read lock.
	assert.NotNil(t, r.EventHandler("order.created"))
}

func TestRegistry_EventHandler_ReadLockCacheHit_Negative(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenEvent("order.*", eventTagger("order.*")))

	assert.Nil(t, r.EventHandler("invoice.created"))
	// Second call returns nil from the negative cache via the read lock.
	assert.Nil(t, r.EventHandler("invoice.created"))
}

func TestRegistry_CommandHandler_ReadLockCacheHit_Positive(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenCommand("invoice.*", cmdTagger("invoice.*")))

	require.NotNil(t, r.CommandHandler("invoice.create"))
	assert.NotNil(t, r.CommandHandler("invoice.create"))
}

func TestRegistry_CommandHandler_ReadLockCacheHit_Negative(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenCommand("invoice.*", cmdTagger("invoice.*")))

	assert.Nil(t, r.CommandHandler("payment.create"))
	assert.Nil(t, r.CommandHandler("payment.create"))
}

func TestRegistry_NotificationHandler_ReadLockCacheHit_Positive(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenNotification("alerts.#", notifTagger("alerts.#")))

	require.NotNil(t, r.NotificationHandler("alerts.security.high"))
	assert.NotNil(t, r.NotificationHandler("alerts.security.high"))
}

func TestRegistry_NotificationHandler_ReadLockCacheHit_Negative(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenNotification("alerts.#", notifTagger("alerts.#")))

	assert.Nil(t, r.NotificationHandler("audits.security.high"))
	assert.Nil(t, r.NotificationHandler("audits.security.high"))
}
