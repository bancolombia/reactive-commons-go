package async

import "errors"

// ErrDuplicateHandler is returned when ListenEvent, ListenCommand, ServeQuery, or
// ListenNotification is called more than once with the same handler name.
var ErrDuplicateHandler = errors.New("reactive-commons: handler already registered for this name")

// ErrQueryTimeout is returned by RequestReply when the context deadline is exceeded
// before a reply is received from the remote handler.
var ErrQueryTimeout = errors.New("reactive-commons: query timed out waiting for reply")

// ErrBrokerUnavailable is returned when a publish or send operation fails because
// the broker connection is not available.
var ErrBrokerUnavailable = errors.New("reactive-commons: broker connection unavailable")

// ErrDeserialize is returned when an incoming message body cannot be unmarshaled
// into the expected target type.
var ErrDeserialize = errors.New("reactive-commons: failed to deserialize message payload")

// ErrWildcardNotSupported is returned by ServeQuery when the query name
// contains a RabbitMQ wildcard ('*' or '#'). Queries are routed through
// the direct exchange and cannot be addressed via topic patterns; this
// matches reactive-commons-java's HandlerResolver.addQueryHandler check.
var ErrWildcardNotSupported = errors.New("reactive-commons: wildcards are not supported in query handler names")
