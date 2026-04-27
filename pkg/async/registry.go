package async

// HandlerRegistry registers message handlers for a service instance.
//
// All registration MUST occur before calling Application.Start(). Handlers
// added after Start() will be stored but not bound on the broker (events and
// notifications won't receive deliveries, since AMQP queue bindings are
// declared once during Start). This differs from reactive-commons-java's
// DynamicRegistry; if you need dynamic bindings, restart the application or
// open a feature request.
type HandlerRegistry interface {
	// ListenEvent registers handler for the named domain event type.
	// Returns ErrDuplicateHandler if a handler for eventName is already registered.
	ListenEvent(eventName string, handler EventHandler[any]) error

	// ListenCommand registers handler for the named command type.
	// Returns ErrDuplicateHandler if a handler for commandName is already registered.
	ListenCommand(commandName string, handler CommandHandler[any]) error

	// ServeQuery registers handler for the named query type.
	// Returns ErrDuplicateHandler if a handler for queryName is already registered.
	ServeQuery(queryName string, handler QueryHandler[any, any]) error

	// ListenNotification registers handler for the named notification type.
	// Returns ErrDuplicateHandler if a handler for notificationName is already registered.
	ListenNotification(notificationName string, handler NotificationHandler[any]) error
}
