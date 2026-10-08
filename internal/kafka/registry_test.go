package kafka

import (
	"context"
	"errors"
	"testing"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cmdHandler returns an error carrying the tag, so tests can identify which
// registered handler was resolved.
func cmdHandler(tag string) async.CommandHandler[any] {
	return func(_ context.Context, _ async.Command[any]) error { return errors.New(tag) }
}

func queryHandler(tag string) async.QueryHandler[any, any] {
	return func(_ context.Context, _ async.AsyncQuery[any], _ async.From) (any, error) { return tag, nil }
}

func TestRegistry_CommandWildcardResolution(t *testing.T) {
	r := newHandlerRegistry()

	hExact := cmdHandler("exact")
	hStar := cmdHandler("star")
	hHash := cmdHandler("hash")
	require.NoError(t, r.ListenCommand("orders.create", hExact))
	require.NoError(t, r.ListenCommand("orders.*", hStar))
	require.NoError(t, r.ListenCommand("orders.#", hHash))

	cases := []struct {
		name string
		want any // marker: the resolved handler's identity
	}{
		{"orders.create", "exact"}, // exact beats both wildcards
		{"orders.cancel", "star"},  // '*' beats '#' in the same position
		{"orders.v2.created", "hash"},
		{"other.thing", nil},
	}
	for _, tc := range cases {
		got := r.CommandHandler(tc.name)
		if tc.want == nil {
			assert.Nil(t, got, tc.name)
			continue
		}
		require.NotNil(t, got, tc.name)
		err := got(context.Background(), async.Command[any]{Name: tc.name})
		require.Error(t, err)
		assert.Equal(t, tc.want, err.Error())
	}
}

func TestRegistry_QueryExactMatchOnly(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ServeQuery("get-product", queryHandler("product")))

	assert.NotNil(t, r.QueryHandler("get-product"))
	assert.Nil(t, r.QueryHandler("get-*"))
	assert.Nil(t, r.QueryHandler("other"))
}

func TestRegistry_RejectsWildcardQueryNames(t *testing.T) {
	r := newHandlerRegistry()
	assert.ErrorIs(t, r.ServeQuery("order.*", queryHandler("x")), async.ErrWildcardNotSupported)
	assert.ErrorIs(t, r.ServeQuery("order.#", queryHandler("x")), async.ErrWildcardNotSupported)
}

func TestRegistry_ClosedAfterMarkStarted(t *testing.T) {
	r := newHandlerRegistry()
	r.markStarted()

	assert.ErrorIs(t, r.ListenCommand("c", cmdHandler("c")), async.ErrRegistrationClosed)
	assert.ErrorIs(t, r.ServeQuery("q", queryHandler("q")), async.ErrRegistrationClosed)
	assert.ErrorIs(t, r.ListenEvent("e", func(_ context.Context, _ async.DomainEvent[any]) error { return nil }), async.ErrRegistrationClosed)
	assert.ErrorIs(t, r.ListenNotification("n", func(_ context.Context, _ async.Notification[any]) error { return nil }), async.ErrRegistrationClosed)
}

func TestRegistry_SnapshotsAreCopies(t *testing.T) {
	r := newHandlerRegistry()
	require.NoError(t, r.ListenCommand("c", cmdHandler("c")))
	require.NoError(t, r.ServeQuery("q", queryHandler("q")))

	cmds := r.snapshotCommandHandlers()
	queries := r.snapshotQueryHandlers()
	assert.Len(t, cmds, 1)
	assert.Len(t, queries, 1)

	delete(cmds, "c")
	delete(queries, "q")
	assert.NotNil(t, r.CommandHandler("c"), "snapshot mutation must not affect the registry")
	assert.NotNil(t, r.QueryHandler("q"))
}
