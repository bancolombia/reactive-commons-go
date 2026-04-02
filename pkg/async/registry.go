package async

// HandlerRegistry registers message handlers for a service instance.
// All registration SHOULD occur before calling Application.Start().
// Dynamic registration after Start() is supported; the implementation MUST
// update queue bindings accordingly while the broker connection is live.
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
