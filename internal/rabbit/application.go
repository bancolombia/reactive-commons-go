package rabbit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/utils"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// RabbitApp is the internal RabbitMQ-backed implementation of async.Application.
type RabbitApp struct {
	cfg        Config
	reg        *handlerRegistry
	log        *slog.Logger
	ready      chan struct{}
	shutdownWg sync.WaitGroup

	mu  sync.RWMutex
	bus async.DomainEventBus
	gw  async.DirectAsyncGateway

	// Stable per-process names for the auto-delete temp queues. These are
	// generated once on Start and reused on every reconnect so the broker
	// re-creates the same queue (the previous one was auto-deleted when the
	// connection dropped) and the gateway's replyQueue reference stays valid.
	replyQueueName        string
	notificationQueueName string

	// Listener references kept across reconnects so bringUpConsumers can
	// restart consumption on a fresh channel after the AMQP library has
	// closed the previous one. Only the kinds that have registered handlers
	// are populated; the others stay nil.
	replyL *replyListener
	notifL *notificationListener
	eventL *eventListener
	cmdL   *commandListener
	qL     *queryListener
}

// NewRabbitApp creates a new RabbitApp. Call Start to connect and begin processing.
func NewRabbitApp(cfg Config) *RabbitApp {
	return &RabbitApp{
		cfg:   cfg,
		reg:   newHandlerRegistry(),
		log:   cfg.Logger,
		ready: make(chan struct{}),
	}
}

func (a *RabbitApp) Registry() async.HandlerRegistry { return a.reg }

func (a *RabbitApp) EventBus() async.DomainEventBus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.bus
}

func (a *RabbitApp) Gateway() async.DirectAsyncGateway {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.gw
}

// Ready returns a channel that is closed when Start has completed topology setup
// and consumers are running. Safe to use EventBus() only after Ready() closes.
func (a *RabbitApp) Ready() <-chan struct{} { return a.ready }

// Start connects to the broker, declares topology, starts consumers, then
// blocks until ctx is cancelled.
func (a *RabbitApp) Start(ctx context.Context) error {
	conn := NewConnection(a.cfg)
	if err := conn.DialContext(ctx); err != nil {
		return err
	}

	a.replyQueueName = utils.GenerateTempName(a.cfg.AppName, "replies")
	if len(a.reg.NotificationNames()) > 0 {
		a.notificationQueueName = utils.GenerateTempName(a.cfg.AppName, "notification")
	}

	sender, err := NewSender(conn, a.cfg)
	if err != nil {
		return err
	}

	gw := newGateway(sender, a.cfg)
	replyRouter := NewReplyRouter()
	gw.withReplySupport(a.replyQueueName, replyRouter)

	a.replyL = newReplyListener(conn, replyRouter, a.cfg, &a.shutdownWg)
	if a.notificationQueueName != "" {
		a.notifL = newNotificationListener(conn, a.reg, a.cfg, &a.shutdownWg)
	}
	if len(a.reg.EventNames()) > 0 {
		a.eventL = newEventListener(conn, a.reg, a.cfg, &a.shutdownWg)
	}
	if len(a.reg.CommandNames()) > 0 {
		a.cmdL = newCommandListener(conn, a.reg, a.cfg, &a.shutdownWg)
	}
	if len(a.reg.QueryNames()) > 0 {
		a.qL = newQueryListener(conn, a.reg, gw, a.cfg, &a.shutdownWg)
	}

	a.mu.Lock()
	a.bus = newEventBus(sender, a.cfg)
	a.gw = gw
	a.mu.Unlock()

	if err := a.bringUpConsumers(ctx, conn); err != nil {
		return err
	}

	// Order matters: rebuild the publisher channel BEFORE redeclaring
	// consumers, so a query that arrives the instant a listener restarts
	// always has a live publisher channel available for gw.Reply.
	conn.OnReconnect(func() {
		if err := sender.Reconnect(); err != nil {
			a.log.Error("reactive-commons: failed to rebuild publisher channel after reconnect", "error", err)
		}
	})
	conn.OnReconnect(func() {
		a.log.Info("reactive-commons: re-declaring topology and restarting consumers after reconnect")
		if err := a.bringUpConsumers(ctx, conn); err != nil {
			a.log.Error("reactive-commons: failed to restore consumers after reconnect", "error", err)
		}
	})

	close(a.ready)

	<-ctx.Done()

	// Graceful shutdown: wait for all in-flight consumer goroutines to finish,
	// with a 30-second hard deadline before forcing the connection closed.
	shutdownDone := make(chan struct{})
	go func() {
		a.shutdownWg.Wait()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(30 * time.Second):
		a.log.Warn("reactive-commons: graceful shutdown timed out after 30s, forcing connection close")
	}
	return conn.Close()
}

// bringUpConsumers (re)declares the consumer-side topology and starts every
// listener. Safe to call both at first connect and from the reconnect hook:
// queue declarations are idempotent for durable queues, and the auto-delete
// temp queues are always recreated under the same stable names. Each listener
// opens a fresh channel and spawns a new goroutine; the previous goroutine
// (if any) has already exited because its deliveries channel was closed by
// the AMQP library when the connection dropped.
func (a *RabbitApp) bringUpConsumers(ctx context.Context, conn *Connection) error {
	topoCh, err := conn.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = topoCh.Close() }()
	topo := NewTopology(a.cfg, topoCh)

	if err := topo.DeclareExchanges(); err != nil {
		return err
	}

	if err := a.declareEvents(topo); err != nil {
		return err
	}
	if err := a.declareCommands(topo); err != nil {
		return err
	}
	if err := a.declareQueries(topo); err != nil {
		return err
	}
	if err := a.declareNotifications(topo); err != nil {
		return err
	}
	if err := a.declareReplies(topo); err != nil {
		return err
	}

	if a.eventL != nil {
		if err := a.eventL.Start(ctx, a.cfg.AppName+".subsEvents"); err != nil {
			return err
		}
	}
	if a.cmdL != nil {
		if err := a.cmdL.Start(ctx, a.cfg.AppName); err != nil {
			return err
		}
	}
	if a.qL != nil {
		if err := a.qL.Start(ctx, a.cfg.AppName+".query"); err != nil {
			return err
		}
	}
	if err := a.replyL.Start(ctx, a.replyQueueName); err != nil {
		return err
	}
	if a.notifL != nil {
		if err := a.notifL.Start(ctx, a.notificationQueueName); err != nil {
			return err
		}
	}
	return nil
}

func (a *RabbitApp) declareEvents(topo *Topology) error {
	names := a.reg.EventNames()
	if len(names) == 0 {
		return nil
	}
	qName, err := topo.DeclareEventsQueue()
	if err != nil {
		return err
	}
	if a.cfg.WithDLQRetry {
		if err = topo.DeclareEventsDLQRetry(qName); err != nil {
			return err
		}
	}
	for _, name := range names {
		if err = topo.BindQueue(qName, a.cfg.DomainEventsExchange, name); err != nil {
			return err
		}
	}
	return nil
}

func (a *RabbitApp) declareCommands(topo *Topology) error {
	if len(a.reg.CommandNames()) == 0 {
		return nil
	}
	qName, err := topo.DeclareCommandsQueue()
	if err != nil {
		return err
	}
	if a.cfg.WithDLQRetry {
		if err = topo.DeclareDirectDLQRetry(qName, a.cfg.AppName); err != nil {
			return err
		}
	}
	return topo.BindQueue(qName, a.cfg.DirectMessagesExchange, a.cfg.AppName)
}

func (a *RabbitApp) declareQueries(topo *Topology) error {
	if len(a.reg.QueryNames()) == 0 {
		return nil
	}
	qName, err := topo.DeclareQueriesQueue()
	if err != nil {
		return err
	}
	if a.cfg.WithDLQRetry {
		if err = topo.DeclareDirectDLQRetry(qName, qName); err != nil {
			return err
		}
	}
	return topo.BindQueue(qName, a.cfg.DirectMessagesExchange, qName)
}

func (a *RabbitApp) declareNotifications(topo *Topology) error {
	if a.notificationQueueName == "" {
		return nil
	}
	if _, err := topo.DeclareTempQueue(a.notificationQueueName); err != nil {
		return err
	}
	for _, name := range a.reg.NotificationNames() {
		if err := topo.BindQueue(a.notificationQueueName, a.cfg.DomainEventsExchange, name); err != nil {
			return err
		}
	}
	return nil
}

func (a *RabbitApp) declareReplies(topo *Topology) error {
	if _, err := topo.DeclareTempQueue(a.replyQueueName); err != nil {
		return err
	}
	return topo.BindQueue(a.replyQueueName, a.cfg.GlobalReplyExchange, a.replyQueueName)
}
