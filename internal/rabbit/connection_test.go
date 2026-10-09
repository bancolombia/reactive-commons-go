package rabbit

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConnection_dialURL_PlainAMQPWithoutTLS(t *testing.T) {
	c := NewConnection(Config{
		Host:     "broker.example.com",
		Port:     5672,
		Username: "user",
		Password: "s3cret",
		VHost:    "/prod",
	})

	assert.Equal(t, "amqp://user:s3cret@broker.example.com:5672/prod", c.dialURL())
}

func TestConnection_dialURL_AMQPSWithTLS(t *testing.T) {
	c := NewConnection(Config{
		Host:     "broker.example.com",
		Port:     5671,
		Username: "user",
		Password: "s3cret",
		VHost:    "/prod",
		TLS:      &tls.Config{},
	})

	assert.Equal(t, "amqps://user:s3cret@broker.example.com:5671/prod", c.dialURL())
}

func TestConnection_dialConfig_PlumbsTLSClientConfig(t *testing.T) {
	tlsCfg := &tls.Config{ServerName: "broker.example.com"}
	c := NewConnection(Config{
		Host: "broker.example.com",
		Port: 5671,
		TLS:  tlsCfg,
	})

	assert.Same(t, tlsCfg, c.dialConfig().TLSClientConfig)

	plain := NewConnection(Config{Host: "localhost", Port: 5672}).dialConfig()
	assert.Nil(t, plain.TLSClientConfig)
}
