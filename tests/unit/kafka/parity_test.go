package kafka_test

import (
	"testing"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplication_ImplementsAsyncApplication is a compile-time and runtime
// assertion that the public Kafka Application is a drop-in async.Application.
// A caller MUST be able to swap the constructor without touching call sites.
func TestApplication_ImplementsAsyncApplication(t *testing.T) {
	t.Parallel()

	var _ async.Application = (*rckafka.Application)(nil)

	app, err := rckafka.NewApplication(minimalConfig())
	require.NoError(t, err)

	var iface async.Application = app
	assert.NotNil(t, iface.Registry(), "Registry() must be non-nil")
	assert.NotNil(t, iface.EventBus(), "EventBus() must be non-nil")
	assert.NotNil(t, iface.Gateway(), "Gateway() must be non-nil")
}
