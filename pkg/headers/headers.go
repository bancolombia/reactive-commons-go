// Package headers defines the AMQP message header constants used by reactive-commons.
// Values MUST match Java Headers.java exactly for wire interoperability.
package headers

const (
	// ReplyID is the reply-queue routing key sent with query requests.
	// Java: Headers.REPLY_ID = "x-reply_id"
	ReplyID = "x-reply_id"

	// CorrelationID correlates a query reply back to its originating request.
	// Java: Headers.CORRELATION_ID = "x-correlation-id"
	CorrelationID = "x-correlation-id"

	// CompletionOnlySignal is set to "true" on a query reply when the response body is nil.
	// Java: Headers.COMPLETION_ONLY_SIGNAL = "x-empty-completion"
	CompletionOnlySignal = "x-empty-completion"

	// ServedQueryID carries the query resource name on a query request.
	// Java: Headers.SERVED_QUERY_ID = "x-serveQuery-id"
	ServedQueryID = "x-serveQuery-id"

	// SourceApplication identifies the publishing application.
	// Java: Headers.SOURCE_APPLICATION = "sourceApplication"
	SourceApplication = "sourceApplication"

	// ReplyTimeoutMillis is the query reply timeout in milliseconds (sent as string).
	// Java: Headers.REPLY_TIMEOUT_MILLIS = "x-reply-timeout-millis"
	ReplyTimeoutMillis = "x-reply-timeout-millis"

	// ReplyError is set to "true" on an error reply from a query handler.
	// The body contains {"errorMessage":"<description>"}.
	ReplyError = "x-reply-error"
)
