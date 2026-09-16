package kafka

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// stubGateway implements async.DirectAsyncGateway and always errors with
// kafka.ErrNotSupportedOnKafka. Kafka does not support commands or async
// queries in this feature.
type stubGateway struct{}

var _ async.DirectAsyncGateway = (*stubGateway)(nil)

func (stubGateway) SendCommand(_ context.Context, _ async.Command[any], _ string) error {
	return fmt.Errorf("kafka: %s: %w", "SendCommand", ErrNotSupportedOnKafka)
}

func (stubGateway) RequestReply(_ context.Context, _ async.AsyncQuery[any], _ string) (json.RawMessage, error) {
	return nil, fmt.Errorf("kafka: %s: %w", "RequestReply", ErrNotSupportedOnKafka)
}

func (stubGateway) Reply(_ context.Context, _ any, _ async.From) error {
	return fmt.Errorf("kafka: %s: %w", "Reply", ErrNotSupportedOnKafka)
}
