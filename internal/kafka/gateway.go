package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/envelope"
	"github.com/bancolombia/reactive-commons-go/internal/replyrouter"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	"github.com/google/uuid"
)

// publisher is the subset of *producer the gateway needs. It exists so the
// gateway's reply-handling logic is unit-testable without a broker.
type publisher interface {
	publish(ctx context.Context, topic string, key, value []byte, headers map[string]string) error
}

// kafkaGateway implements async.DirectAsyncGateway over a producer: commands
// go to the target's commands topic, queries to the target's queries topic,
// and replies to the requester's reply topic (from.ReplyID).
type kafkaGateway struct {
	cfg          Config
	pub          publisher
	obs          *observability
	router       *replyrouter.ReplyRouter
	replyEnabled bool
	replyReady   <-chan struct{} // closed once the reply consumer has primed
}

var _ async.DirectAsyncGateway = (*kafkaGateway)(nil)

func newGateway(cfg Config, pub publisher, router *replyrouter.ReplyRouter, replyReady <-chan struct{}, obs *observability) *kafkaGateway {
	return &kafkaGateway{
		cfg:          cfg,
		pub:          pub,
		obs:          obs,
		router:       router,
		replyEnabled: !cfg.DisableReplyListener,
		replyReady:   replyReady,
	}
}

// SendCommand publishes cmd to the target's commands topic. Blocks until the
// broker acks per cfg.ProducerAcks. Returns ErrPayloadTooLarge if the
// marshalled envelope exceeds cfg.MaxMessageBytes.
func (g *kafkaGateway) SendCommand(ctx context.Context, cmd async.Command[any], targetService string) error {
	if targetService == "" {
		return fmt.Errorf("kafka: SendCommand: targetService is required")
	}
	if cmd.Name == "" {
		return fmt.Errorf("kafka: SendCommand: command name is required")
	}
	body, err := envelope.Marshal(cmd)
	if err != nil {
		return err
	}
	if g.cfg.MaxMessageBytes > 0 && len(body) > g.cfg.MaxMessageBytes {
		return fmt.Errorf("%w: envelope size %d > %d", ErrPayloadTooLarge, len(body), g.cfg.MaxMessageBytes)
	}
	topic := topicForCommand(g.cfg, targetService)
	headers := rcHeaders("command", g.cfg.AppName)
	spanCtx, end := g.obs.startProduceSpan(ctx, topic, "command")
	err = g.pub.publish(spanCtx, topic, []byte(cmd.CommandID), body, headers)
	end(err)
	if err == nil {
		g.obs.messagesEmitted.Add(ctx, 1, metricAttrs("kafka", topic, "command"))
	}
	return err
}

// RequestReply publishes a query to the target's queries topic and blocks
// until a reply is routed back to this instance, or ctx is exceeded. The
// query is only published after this instance's reply consumer has primed,
// so a reply produced in response can never be missed.
func (g *kafkaGateway) RequestReply(ctx context.Context, query async.AsyncQuery[any], targetService string) (json.RawMessage, error) {
	if !g.replyEnabled {
		return nil, fmt.Errorf("kafka: RequestReply unavailable: DisableReplyListener is set")
	}
	if targetService == "" {
		return nil, fmt.Errorf("kafka: RequestReply: targetService is required")
	}
	if err := g.awaitReplyReady(ctx); err != nil {
		return nil, err
	}

	correlationID := uuid.New().String()
	replyCh := g.router.Register(correlationID)
	defer g.router.Deregister(correlationID)

	body, err := envelope.Marshal(query)
	if err != nil {
		return nil, err
	}
	if g.cfg.MaxMessageBytes > 0 && len(body) > g.cfg.MaxMessageBytes {
		return nil, fmt.Errorf("%w: envelope size %d > %d", ErrPayloadTooLarge, len(body), g.cfg.MaxMessageBytes)
	}

	timeoutMs := "0"
	if deadline, ok := ctx.Deadline(); ok {
		ms := time.Until(deadline).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		timeoutMs = strconv.FormatInt(ms, 10)
	}

	topic := topicForQuery(g.cfg, targetService)
	headers := rcHeaders("query", g.cfg.AppName)
	headers[hdr.ReplyID] = topicForReply(g.cfg)
	headers[hdr.CorrelationID] = correlationID
	headers[hdr.ServedQueryID] = query.Resource
	headers[hdr.ReplyTimeoutMillis] = timeoutMs

	spanCtx, end := g.obs.startProduceSpan(ctx, topic, "query")
	err = g.pub.publish(spanCtx, topic, []byte(correlationID), body, headers)
	end(err)
	if err != nil {
		return nil, err
	}
	g.obs.messagesEmitted.Add(ctx, 1, metricAttrs("kafka", topic, "query"))

	return replyrouter.AwaitReply(ctx, replyCh)
}

// Reply publishes the query response to the requesting instance's reply topic
// (from.ReplyID). Pass nil response to send a completion-only signal.
func (g *kafkaGateway) Reply(ctx context.Context, response any, from async.From) error {
	if from.CorrelationID == "" || from.ReplyID == "" {
		return fmt.Errorf("kafka: Reply: missing reply routing metadata (correlationId=%q, replyId=%q)",
			from.CorrelationID, from.ReplyID)
	}
	var body []byte
	var err error
	headers := rcHeaders("reply", g.cfg.AppName)
	headers[hdr.CorrelationID] = from.CorrelationID
	if response == nil {
		body = []byte("null")
		headers[hdr.CompletionOnlySignal] = "true"
	} else {
		body, err = json.Marshal(response)
		if err != nil {
			return fmt.Errorf("reactive-commons: marshal query reply: %w", err)
		}
	}
	return g.publishReply(ctx, from.ReplyID, from.CorrelationID, body, headers)
}

// replyError sends an error reply to the caller (used by the query listener).
func (g *kafkaGateway) replyError(ctx context.Context, errMsg string, from async.From) error {
	if from.CorrelationID == "" || from.ReplyID == "" {
		return fmt.Errorf("kafka: replyError: missing reply routing metadata (correlationId=%q, replyId=%q)",
			from.CorrelationID, from.ReplyID)
	}
	body, err := json.Marshal(map[string]string{"errorMessage": errMsg})
	if err != nil {
		return fmt.Errorf("reactive-commons: marshal error reply: %w", err)
	}
	headers := rcHeaders("reply", g.cfg.AppName)
	headers[hdr.CorrelationID] = from.CorrelationID
	headers[hdr.ReplyError] = "true"
	return g.publishReply(ctx, from.ReplyID, from.CorrelationID, body, headers)
}

func (g *kafkaGateway) publishReply(ctx context.Context, topic, correlationID string, body []byte, headers map[string]string) error {
	spanCtx, end := g.obs.startProduceSpan(ctx, topic, "reply")
	err := g.pub.publish(spanCtx, topic, []byte(correlationID), body, headers)
	end(err)
	if err == nil {
		g.obs.messagesEmitted.Add(ctx, 1, metricAttrs("kafka", topic, "reply"))
	}
	return err
}

// awaitReplyReady blocks until the reply consumer has completed its first
// fetch cycle (offset anchoring). The ctx deadline bounds the wait; expiry
// surfaces as async.ErrQueryTimeout to match the reply-wait semantics.
func (g *kafkaGateway) awaitReplyReady(ctx context.Context) error {
	if g.replyReady == nil {
		return nil
	}
	select {
	case <-g.replyReady:
		return nil
	case <-ctx.Done():
		return async.ErrQueryTimeout
	}
}

// rcHeaders builds the reactive-commons wire headers shared by commands,
// queries, and replies.
func rcHeaders(kind, appName string) map[string]string {
	return map[string]string{
		"content-type":              "application/json",
		"reactive-commons-envelope": "v1",
		"reactive-commons-kind":     kind,
		"reactive-commons-version":  libraryVersion,
		hdr.SourceApplication:       appName,
	}
}

// notReadyGateway is the pre-Start stub for async.DirectAsyncGateway. It is
// replaced by the real kafkaGateway once Start dials the brokers.
type notReadyGateway struct{}

var _ async.DirectAsyncGateway = (*notReadyGateway)(nil)

func (notReadyGateway) SendCommand(_ context.Context, _ async.Command[any], _ string) error {
	return fmt.Errorf("kafka: Gateway not ready; wait for <-app.Ready() before sending commands")
}

func (notReadyGateway) RequestReply(_ context.Context, _ async.AsyncQuery[any], _ string) (json.RawMessage, error) {
	return nil, fmt.Errorf("kafka: Gateway not ready; wait for <-app.Ready() before sending queries")
}

func (notReadyGateway) Reply(_ context.Context, _ any, _ async.From) error {
	return fmt.Errorf("kafka: Gateway not ready; wait for <-app.Ready() before replying")
}
