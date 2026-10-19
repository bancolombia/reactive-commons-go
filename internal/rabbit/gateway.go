package rabbit

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/replyrouter"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

// messageSender is the subset of *Sender the gateway needs. Declared as an
// interface so tests can capture the context and headers passed to a send.
type messageSender interface {
	SendWithConfirm(ctx context.Context, body []byte, exchange, routingKey string, headers amqp.Table, persistent bool) error
	SendNoConfirm(ctx context.Context, body []byte, exchange, routingKey string, headers amqp.Table, persistent bool) error
}

// gateway implements async.DirectAsyncGateway.
type gateway struct {
	sender      messageSender
	cfg         Config
	replyQueue  string                   // name of this instance's reply queue (set after Start)
	replyRouter *replyrouter.ReplyRouter // routes replies to waiting callers
}

func newGateway(sender messageSender, cfg Config) *gateway {
	return &gateway{sender: sender, cfg: cfg}
}

func (g *gateway) withReplySupport(replyQueue string, router *replyrouter.ReplyRouter) {
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

	// ReplyTimeout is the documented default for RequestReply when the caller
	// supplies no deadline. A caller deadline is never shortened or extended.
	replyCtx := ctx
	if _, ok := replyCtx.Deadline(); !ok && g.cfg.ReplyTimeout > 0 {
		var cancel context.CancelFunc
		replyCtx, cancel = context.WithTimeout(replyCtx, g.cfg.ReplyTimeout)
		defer cancel()
	}

	timeoutMs := "0"
	if deadline, ok := replyCtx.Deadline(); ok {
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
	if err := g.sender.SendWithConfirm(replyCtx, body, g.cfg.DirectMessagesExchange, routingKey, headers, g.cfg.PersistentQueries); err != nil {
		return nil, err
	}

	return replyrouter.AwaitReply(replyCtx, replyCh)
}

// Reply sends the query response to the original caller identified by from.
// Pass nil response to send a completion-only signal. Replies use publisher
// confirms so the send either reaches the broker or surfaces an error —
// a silently lost reply would otherwise look like a caller timeout.
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

	return g.sender.SendWithConfirm(ctx, body, g.cfg.GlobalReplyExchange, from.ReplyID, headers, false)
}

// replyError sends an error reply to the caller (used by the query listener).
// replyContext derives the context used to send a query reply. The caller's
// x-reply-timeout-millis header wins when positive; otherwise ReplyTimeout is
// used. The handler itself is not bounded by this context — only the reply
// send and its confirm wait are.
func (g *gateway) replyContext(ctx context.Context, headers amqp.Table) (context.Context, context.CancelFunc) {
	if d, ok := headerDurationMillis(headers, hdr.ReplyTimeoutMillis); ok {
		return context.WithTimeout(ctx, d)
	}
	if g.cfg.ReplyTimeout > 0 {
		return context.WithTimeout(ctx, g.cfg.ReplyTimeout)
	}
	return ctx, func() {}
}

// headerDurationMillis parses a millisecond duration from an AMQP header,
// accepting the string form this library sends and numeric forms other
// reactive-commons clients may send.
func headerDurationMillis(headers amqp.Table, key string) (time.Duration, bool) {
	if headers == nil {
		return 0, false
	}
	v, ok := headers[key]
	if !ok || v == nil {
		return 0, false
	}

	var ms int64
	switch n := v.(type) {
	case string:
		parsed, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0, false
		}
		ms = parsed
	case []byte:
		parsed, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil {
			return 0, false
		}
		ms = parsed
	case int:
		ms = int64(n)
	case int32:
		ms = int64(n)
	case int64:
		ms = n
	case float64:
		ms = int64(n)
	default:
		return 0, false
	}
	if ms <= 0 {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

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
	return g.sender.SendWithConfirm(ctx, body, g.cfg.GlobalReplyExchange, from.ReplyID, headers, false)
}
