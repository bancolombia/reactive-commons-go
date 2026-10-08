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

	topics := make([]string, 0, len(handlers)*2+len(notifHandlers)+3)
	for name := range handlers {
		topics = append(topics, topicForEvent(a.cfg, name))
		topics = append(topics, topicForEvent(a.cfg, name)+a.cfg.DLQSuffix)
	}
	for name := range notifHandlers {
		topics = append(topics, topicForNotification(a.cfg, name))
	}
	if len(cmdHandlers) > 0 {
		cmdTopic := topicForCommand(a.cfg, a.cfg.AppName)
		topics = append(topics, cmdTopic, cmdTopic+a.cfg.DLQSuffix)
	}
	if len(queryHandlers) > 0 {
		topics = append(topics, topicForQuery(a.cfg, a.cfg.AppName))
	}
	replyEnabled := !a.cfg.DisableReplyListener
	if replyEnabled {
		topics = append(topics, topicForReply(a.cfg))
	}
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

	for _, l := range listeners {
		a.wg.Add(1)
		go func(lst *eventListener) {
			defer a.wg.Done()
			lst.run(ctx)
		}(l)
	}
	for _, l := range notifListeners {
		a.wg.Add(1)
		go func(lst *notificationListener) {
			defer a.wg.Done()
			lst.run(ctx)
		}(l)
	}
	if cmdL != nil {
		a.wg.Add(1)
		go func(lst *commandListener) {
			defer a.wg.Done()
			lst.run(ctx)
		}(cmdL)
	}
	if queryL != nil {
		a.wg.Add(1)
		go func(lst *queryListener) {
			defer a.wg.Done()
			lst.run(ctx)
		}(queryL)
	}
	if replyL != nil {
		a.wg.Add(1)
		go func(lst *replyListener) {
			defer a.wg.Done()
			lst.run(ctx)
		}(replyL)
	}

	close(a.ready)

	<-ctx.Done()

	// Shutdown ordering per spec:
	// (1) per-listener ctx already cancelled via ctx.Done;
	// (2) wait for handler goroutines up to HandlerTimeout;
	// (3) close readers;
	// (4) close the producer last since DLQ and reply paths use it.
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
		if err := l.close(); err != nil {
			a.logger.Warn("kafka: listener close error", "topic", l.topic, "err", err)
		}
	}
	for _, l := range notifListeners {
		if err := l.close(); err != nil {
			a.logger.Warn("kafka: notification listener close error", "topic", l.topic, "err", err)
		}
	}
	if cmdL != nil {
		if err := cmdL.close(); err != nil {
			a.logger.Warn("kafka: command listener close error", "topic", cmdL.topic, "err", err)
		}
	}
	if queryL != nil {
		if err := queryL.close(); err != nil {
			a.logger.Warn("kafka: query listener close error", "topic", queryL.topic, "err", err)
		}
	}
	if replyL != nil {
		if err := replyL.close(); err != nil {
			a.logger.Warn("kafka: reply listener close error", "topic", replyL.topic, "err", err)
		}
	}
	if err := p.close(); err != nil {
		a.logger.Warn("kafka: producer close error", "err", err)
	}
	return nil
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
