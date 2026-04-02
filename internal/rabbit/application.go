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

	topoCh, err := conn.Channel()
	if err != nil {
		return err
	}
	topo := NewTopology(a.cfg, topoCh)

	if err = topo.DeclareExchanges(); err != nil {
		return err
	}

	sender, err := NewSender(conn, a.cfg)
	if err != nil {
		return err
	}

	gw := newGateway(sender, a.cfg)

	// Declare the per-instance reply queue and wire reply routing.
	replyQueue, err := topo.DeclareTempQueue(utils.GenerateTempName(a.cfg.AppName, "replies"))
	if err != nil {
		return err
	}
	if err := topo.BindQueue(replyQueue, a.cfg.GlobalReplyExchange, replyQueue); err != nil {
		return err
	}
	replyRouter := NewReplyRouter()
	gw.withReplySupport(replyQueue, replyRouter)

	// Register topology re-declaration hook for auto-reconnect.
	conn.OnReconnect(func() {
		a.log.Info("reactive-commons: re-declaring topology after reconnect")
		if tCh, chErr := conn.Channel(); chErr == nil {
			reTopoErr := NewTopology(a.cfg, tCh).DeclareExchanges()
			_ = tCh.Close()
			if reTopoErr != nil {
				a.log.Error("reactive-commons: failed to re-declare exchanges after reconnect", "error", reTopoErr)
			}
		}
	})

	a.mu.Lock()
	a.bus = newEventBus(sender, a.cfg)
	a.gw = gw
	a.mu.Unlock()

	if err := a.startEventConsumer(ctx, conn, topo); err != nil {
		return err
	}

	if err := a.startCommandConsumer(ctx, conn, topo); err != nil {
		return err
	}

	if err := a.startQueryConsumer(ctx, conn, topo, gw); err != nil {
		return err
	}

	if err := newReplyListener(conn, replyRouter, a.cfg, &a.shutdownWg).Start(ctx, replyQueue); err != nil {
		return err
	}

	if err := a.startNotificationConsumer(ctx, conn, topo); err != nil {
		return err
	}

	_ = topoCh.Close()
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

func (a *RabbitApp) startQueryConsumer(ctx context.Context, conn *Connection, topo *Topology, gw *gateway) error {
	if len(a.reg.QueryNames()) == 0 {
		return nil
	}
	qName, err := topo.DeclareQueriesQueue()
	if err != nil {
		return err
	}
	if err = topo.BindQueue(qName, a.cfg.DirectMessagesExchange, qName); err != nil {
		return err
	}
	listener := newQueryListener(conn, a.reg, gw, a.cfg, &a.shutdownWg)
	return listener.Start(ctx, qName)
}

func (a *RabbitApp) startCommandConsumer(ctx context.Context, conn *Connection, topo *Topology) error {
	if len(a.reg.CommandNames()) == 0 {
		return nil
	}
	qName, err := topo.DeclareCommandsQueue()
	if err != nil {
		return err
	}
	if a.cfg.WithDLQRetry {
		if err = topo.DeclareDLQ(qName, a.cfg.DirectMessagesExchange); err != nil {
			return err
		}
	}
	// Bind the commands queue to directMessages exchange so messages routed to
	// this service's AppName are delivered here.
	if err = topo.BindQueue(qName, a.cfg.DirectMessagesExchange, a.cfg.AppName); err != nil {
		return err
	}
	listener := newCommandListener(conn, a.reg, a.cfg, &a.shutdownWg)
	return listener.Start(ctx, qName)
}

func (a *RabbitApp) startNotificationConsumer(ctx context.Context, conn *Connection, topo *Topology) error {
	names := a.reg.NotificationNames()
	if len(names) == 0 {
		return nil
	}

	// Each app instance gets its own exclusive auto-delete queue for fan-out delivery.
	qName, err := topo.DeclareTempQueue(utils.GenerateTempName(a.cfg.AppName, "notification"))
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := topo.BindQueue(qName, a.cfg.DomainEventsExchange, name); err != nil {
			return err
		}
	}

	listener := newNotificationListener(conn, a.reg, a.cfg, &a.shutdownWg)
	return listener.Start(ctx, qName)
}

func (a *RabbitApp) startEventConsumer(ctx context.Context, conn *Connection, topo *Topology) error {
	names := a.reg.EventNames()
	if len(names) == 0 {
		return nil
	}

	qName, err := topo.DeclareEventsQueue()
	if err != nil {
		return err
	}
	if a.cfg.WithDLQRetry {
		if err = topo.DeclareDLQ(qName, a.cfg.DomainEventsExchange); err != nil {
			return err
		}
	}
	for _, name := range names {
		if err = topo.BindQueue(qName, a.cfg.DomainEventsExchange, name); err != nil {
			return err
		}
	}

	listener := newEventListener(conn, a.reg, a.cfg, &a.shutdownWg)
	return listener.Start(ctx, qName)
}
