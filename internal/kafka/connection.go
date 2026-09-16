package kafka

import (
	"time"

	kgo "github.com/segmentio/kafka-go"
)

// dialer returns a *kafka.Dialer assembled from cfg's TLS + SASL settings.
// Used by both topology verification and by listener/producer construction.
func dialer(cfg Config) *kgo.Dialer {
	d := &kgo.Dialer{
		Timeout:   10 * time.Second,
		DualStack: true,
		ClientID:  cfg.ClientID,
	}
	if cfg.TLS != nil {
		d.TLS = cfg.TLS
	}
	if cfg.SASL != nil {
		d.SASLMechanism = cfg.SASL
	}
	return d
}
