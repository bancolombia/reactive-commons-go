package rabbit

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

func TestHeaderString_NilHeaders(t *testing.T) {
	assert.Equal(t, "", headerString(nil, "x-correlation-id"))
}

func TestHeaderString_MissingKey(t *testing.T) {
	headers := amqp.Table{"some-other-key": "value"}
	assert.Equal(t, "", headerString(headers, "x-correlation-id"))
}

func TestHeaderString_StringValue(t *testing.T) {
	headers := amqp.Table{"x-correlation-id": "corr-123"}
	assert.Equal(t, "corr-123", headerString(headers, "x-correlation-id"))
}

func TestHeaderString_NonStringValue_ReturnsEmpty(t *testing.T) {
	headers := amqp.Table{"x-correlation-id": 12345}
	assert.Equal(t, "", headerString(headers, "x-correlation-id"))
}

func TestHeaderString_NilValue_ReturnsEmpty(t *testing.T) {
	headers := amqp.Table{"x-correlation-id": nil}
	assert.Equal(t, "", headerString(headers, "x-correlation-id"))
}
