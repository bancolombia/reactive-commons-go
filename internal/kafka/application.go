package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/replyrouter"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// KafkaApp owns all mutable state for a Kafka-backed reactive-commons
// application. Before Start it exposes not-ready stubs for the event bus and
// gateway; Start wires the producer, gateway, and one listener per registered
// handler kind (events, notifications, commands, queries, replies).
type KafkaApp struct {
	cfg      Config
	registry *handlerRegistry
	gateway  async.DirectAsyncGateway
	ready    chan struct{}
	logger   *slog.Logger
	wg       sync.WaitGroup

	obs *observability

	mu             sync.Mutex
	producer       *producer
	eventBus       async.DomainEventBus
	listeners      []*eventListener
	notifListeners []*notificationListener
	cmdListener    *commandListener
	queryListener  *queryListener
	replyListener  *replyListener
}

// NewKafkaApp constructs a KafkaApp from an internal Config.
func NewKafkaApp(cfg Config) *KafkaApp {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &KafkaApp{
		cfg:      cfg,
		registry: newHandlerRegistry(),
		gateway:  notReadyGateway{},
		ready:    make(chan struct{}),
		logger:   log,
		obs:      resolveObservability(cfg),
		eventBus: notReadyBus{},
	}
}

func (a *KafkaApp) Registry() async.HandlerRegistry { return a.registry }
func (a *KafkaApp) Ready() <-chan struct{}          { return a.ready }

// Gateway returns the DirectAsyncGateway. Before Start closes Ready() this
// returns a stub that errors on any call; after Start it returns the real
// producer-backed gateway.
func (a *KafkaApp) Gateway() async.DirectAsyncGateway {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gateway
}

// EventBus returns the DomainEventBus. Before Start closes Ready() this
// returns a stub that errors on any call; after Start it returns the real
// producer-backed bus.
func (a *KafkaApp) EventBus() async.DomainEventBus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.eventBus
}

// Start validates broker connectivity, constructs the producer, publishes the
// real event bus and gateway, opens one listener per registered handler kind,
// closes Ready, and blocks on ctx.Done. On shutdown it drains handler
// goroutines (bounded by HandlerTimeout), closes readers, then closes the
// producer.
func (a *KafkaApp) Start(ctx context.Context) error {
	if err := verifyDial(ctx, a.cfg); err != nil {
		return err
	}

	p := newProducer(a.cfg)
	bus := newDomainEventBus(a.cfg, p, a.obs)

	handlers := a.registry.snapshotEventHandlers()
	notifHandlers := a.registry.snapshotNotificationHandlers()
	cmdHandlers := a.registry.snapshotCommandHandlers()
	queryHandlers := a.registry.snapshotQueryHandlers()
	replyEnabled := !a.cfg.DisableReplyListener

	topics := collectTopicsToVerify(a.cfg, handlers, notifHandlers, cmdHandlers, queryHandlers, replyEnabled)
	if err := verifyTopics(ctx, a.cfg, topics); err != nil {
		_ = p.close()
		return err
	}

	router := replyrouter.NewReplyRouter()

	var replyL *replyListener
	var replyReady <-chan struct{}
	if replyEnabled {
		partitions, err := replyTopicPartitions(ctx, a.cfg)
		if err != nil {
			a.logger.Warn("kafka: reply topic partition lookup failed; assuming 1",
				"topic", topicForReply(a.cfg), "err", err)
		}
		replyL = newReplyListener(a.cfg, router, partitions, a.logger, a.obs)
		replyL.open()
		replyReady = replyL.primed
	}

	gw := newGateway(a.cfg, p, router, replyReady, a.obs)

	var cmdL *commandListener
	if len(cmdHandlers) > 0 {
		cmdL = newCommandListener(a.cfg, a.registry, p, a.logger, a.obs)
		cmdL.open()
	}

	var queryL *queryListener
	if len(queryHandlers) > 0 {
		queryL = newQueryListener(a.cfg, a.registry, gw, a.logger, a.obs)
		queryL.open()
	}

	listeners := make([]*eventListener, 0, len(handlers))
	for name, h := range handlers {
		l := newEventListener(a.cfg, name, h, p, a.logger, a.obs)
		l.open()
		listeners = append(listeners, l)
	}

	notifListeners := make([]*notificationListener, 0, len(notifHandlers))
	for name, h := range notifHandlers {
		l := newNotificationListener(a.cfg, name, h, a.logger, a.obs)
		l.open()
		notifListeners = append(notifListeners, l)
	}

	a.mu.Lock()
	a.producer = p
	a.eventBus = bus
	a.gateway = gw
	a.listeners = listeners
	a.notifListeners = notifListeners
	a.cmdListener = cmdL
	a.queryListener = queryL
	a.replyListener = replyL
	a.mu.Unlock()

	a.registry.markStarted()
	a.spawnListeners(ctx, listeners, notifListeners, cmdL, queryL, replyL)
	close(a.ready)

	<-ctx.Done()

	a.shutdown(p, listeners, notifListeners, cmdL, queryL, replyL)
	return nil
}

// collectTopicsToVerify builds the full list of topics Start must verify
// against the brokers before publishing the real gateway. The value types of
// the handler maps are unused (only key presence and length matter), so the
// function is generic over them.
func collectTopicsToVerify[E, N, C, Q any](
	cfg Config,
	events map[string]E,
	notifs map[string]N,
	commands map[string]C,
	queries map[string]Q,
	replyEnabled bool,
) []string {
	topics := make([]string, 0, len(events)*2+len(notifs)+3)
	for name := range events {
		topics = append(topics, topicForEvent(cfg, name))
		topics = append(topics, topicForEvent(cfg, name)+cfg.DLQSuffix)
	}
	for name := range notifs {
		topics = append(topics, topicForNotification(cfg, name))
	}
	if len(commands) > 0 {
		cmdTopic := topicForCommand(cfg, cfg.AppName)
		topics = append(topics, cmdTopic, cmdTopic+cfg.DLQSuffix)
	}
	if len(queries) > 0 {
		topics = append(topics, topicForQuery(cfg, cfg.AppName))
	}
	if replyEnabled {
		topics = append(topics, topicForReply(cfg))
	}
	return topics
}

// spawn runs run(ctx) in a goroutine tracked by the app's wait group.
func (a *KafkaApp) spawn(ctx context.Context, run func(context.Context)) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		run(ctx)
	}()
}

func (a *KafkaApp) spawnListeners(
	ctx context.Context,
	listeners []*eventListener,
	notifListeners []*notificationListener,
	cmdL *commandListener,
	queryL *queryListener,
	replyL *replyListener,
) {
	for _, l := range listeners {
		a.spawn(ctx, l.run)
	}
	for _, l := range notifListeners {
		a.spawn(ctx, l.run)
	}
	if cmdL != nil {
		a.spawn(ctx, cmdL.run)
	}
	if queryL != nil {
		a.spawn(ctx, queryL.run)
	}
	if replyL != nil {
		a.spawn(ctx, replyL.run)
	}
}

// shutdown implements the shutdown ordering spec:
// (1) per-listener ctx is already cancelled via ctx.Done at the call site;
// (2) wait for handler goroutines up to HandlerTimeout;
// (3) close readers;
// (4) close the producer last since DLQ and reply paths use it.
func (a *KafkaApp) shutdown(
	p *producer,
	listeners []*eventListener,
	notifListeners []*notificationListener,
	cmdL *commandListener,
	queryL *queryListener,
	replyL *replyListener,
) {
	drained := make(chan struct{})
	go func() { a.wg.Wait(); close(drained) }()
	timeout := a.cfg.HandlerTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	select {
	case <-drained:
	case <-time.After(timeout):
		a.logger.Warn("kafka: handler drain timed out", "timeout", timeout)
	}
	for _, l := range listeners {
		a.logCloseErr("listener", l.topic, l.close())
	}
	for _, l := range notifListeners {
		a.logCloseErr("notification listener", l.topic, l.close())
	}
	if cmdL != nil {
		a.logCloseErr("command listener", cmdL.topic, cmdL.close())
	}
	if queryL != nil {
		a.logCloseErr("query listener", queryL.topic, queryL.close())
	}
	if replyL != nil {
		a.logCloseErr("reply listener", replyL.topic, replyL.close())
	}
	if err := p.close(); err != nil {
		a.logger.Warn("kafka: producer close error", "err", err)
	}
}

func (a *KafkaApp) logCloseErr(label, topic string, err error) {
	if err != nil {
		a.logger.Warn("kafka: "+label+" close error", "topic", topic, "err", err)
	}
}

func verifyDial(ctx context.Context, cfg Config) error {
	d := dialer(cfg)
	var lastErr error
	for _, addr := range cfg.BootstrapBrokers {
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.Close()
		return nil
	}
	return fmt.Errorf("%w: %v", ErrBrokerUnreachable, lastErr)
}

// notReadyBus is the pre-Start stub for async.DomainEventBus. It is replaced
// by the real domainEventBus once Start dials the brokers.
type notReadyBus struct{}

func (notReadyBus) Emit(_ context.Context, _ async.DomainEvent[any]) error {
	return fmt.Errorf("kafka: EventBus not ready; wait for <-app.Ready() before emitting")
}

func (notReadyBus) EmitNotification(_ context.Context, _ async.Notification[any]) error {
	return fmt.Errorf("kafka: EventBus not ready; wait for <-app.Ready() before emitting")
}
