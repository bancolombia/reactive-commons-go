package rabbit

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

// gateway implements async.DirectAsyncGateway.
type gateway struct {
	sender      *Sender
	cfg         Config
	replyQueue  string       // name of this instance's reply queue (set after Start)
	replyRouter *ReplyRouter // routes replies to waiting callers
}

func newGateway(sender *Sender, cfg Config) *gateway {
	return &gateway{sender: sender, cfg: cfg}
}

func (g *gateway) withReplySupport(replyQueue string, router *ReplyRouter) {
	g.replyQueue = replyQueue
	g.replyRouter = router
}

// SendCommand delivers cmd to the named targetService via the directMessages exchange.
// The routing key equals targetService, which routes to that service's commands queue.
func (g *gateway) SendCommand(ctx context.Context, cmd async.Command[any], targetService string) error {
	body, err := ToMessage(cmd)
	if err != nil {
		return err
	}
	headers := amqp.Table{hdr.SourceApplication: g.cfg.AppName}
	return g.sender.SendWithConfirm(ctx, body, g.cfg.DirectMessagesExchange, targetService, headers, g.cfg.PersistentCommands)
}

// RequestReply publishes a query to targetService.query and blocks until a reply is
// received via the reply queue, or ctx is exceeded.
func (g *gateway) RequestReply(ctx context.Context, query async.AsyncQuery[any], targetService string) (json.RawMessage, error) {
	if g.replyQueue == "" || g.replyRouter == nil {
		return nil, fmt.Errorf("reactive-commons: reply queue not initialized — call Start() first")
	}

	correlationID := uuid.New().String()
	replyCh := g.replyRouter.Register(correlationID)
	defer g.replyRouter.Deregister(correlationID)

	body, err := ToMessage(query)
	if err != nil {
		return nil, err
	}

	timeoutMs := "0"
	if deadline, ok := ctx.Deadline(); ok {
		ms := time.Until(deadline).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		timeoutMs = strconv.FormatInt(ms, 10)
	}

	headers := amqp.Table{
		hdr.ReplyID:            g.replyQueue,
		hdr.CorrelationID:      correlationID,
		hdr.ServedQueryID:      query.Resource,
		hdr.ReplyTimeoutMillis: timeoutMs,
		hdr.SourceApplication:  g.cfg.AppName,
	}

	routingKey := targetService + ".query"
	if err := g.sender.SendWithConfirm(ctx, body, g.cfg.DirectMessagesExchange, routingKey, headers, g.cfg.PersistentQueries); err != nil {
		return nil, err
	}

	select {
	case p := <-replyCh:
		if p.IsError {
			var errBody struct {
				ErrorMessage string `json:"errorMessage"`
			}
			// Best-effort unmarshal — use raw body as fallback if it fails.
			if unmarshalErr := json.Unmarshal(p.Body, &errBody); unmarshalErr != nil {
				return nil, fmt.Errorf("reactive-commons: query handler error: %s", p.Body)
			}
			return nil, fmt.Errorf("reactive-commons: query handler error: %s", errBody.ErrorMessage)
		}
		if p.IsEmpty {
			return nil, nil
		}
		return json.RawMessage(p.Body), nil
	case <-ctx.Done():
		return nil, async.ErrQueryTimeout
	}
}

// Reply sends the query response to the original caller identified by from.
// Pass nil response to send a completion-only signal.
func (g *gateway) Reply(ctx context.Context, response any, from async.From) error {
	var body []byte
	var err error
	headers := amqp.Table{
		hdr.CorrelationID:     from.CorrelationID,
		hdr.SourceApplication: g.cfg.AppName,
	}

	if response == nil {
		body = []byte("null")
		headers[hdr.CompletionOnlySignal] = "true"
	} else {
		body, err = json.Marshal(response)
		if err != nil {
			return fmt.Errorf("reactive-commons: marshal query reply: %w", err)
		}
	}

	return g.sender.SendNoConfirm(ctx, body, g.cfg.GlobalReplyExchange, from.ReplyID, headers, false)
}

// replyError sends an error reply to the caller (used by the query listener).
func (g *gateway) replyError(ctx context.Context, errMsg string, from async.From) error {
	body, err := json.Marshal(map[string]string{"errorMessage": errMsg})
	if err != nil {
		return fmt.Errorf("reactive-commons: marshal error reply: %w", err)
	}
	headers := amqp.Table{
		hdr.CorrelationID:     from.CorrelationID,
		hdr.ReplyError:        "true",
		hdr.SourceApplication: g.cfg.AppName,
	}
	return g.sender.SendNoConfirm(ctx, body, g.cfg.GlobalReplyExchange, from.ReplyID, headers, false)
}
