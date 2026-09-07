package server

import (
	"time"

	"github.com/psyb0t/aichteeteapee"
)

// Header names, auth schemes, and content types come from aichteeteapee rather
// than being retyped here. Only X-Session-ID is Peen's own, so it is the only
// one defined locally.
const (
	apiBaseURL = "/v1"

	headerAuthorization = aichteeteapee.HeaderNameAuthorization
	headerRequestID     = aichteeteapee.HeaderNameXRequestID
	headerAccept        = aichteeteapee.HeaderNameAccept
	headerContentType   = aichteeteapee.HeaderNameContentType

	// headerSessionID selects the durable session and has no shared-library
	// equivalent because it is specific to Peen's contract.
	headerSessionID = "X-Session-ID"

	// bearerScheme is the scheme TOKEN, without the trailing space that
	// aichteeteapee.AuthSchemeBearer carries for prefix trimming. This code
	// splits the header on its space and compares the token, which rejects
	// malformed values that a prefix trim would accept.
	bearerScheme = "Bearer"

	mediaTypeJSON = aichteeteapee.ContentTypeJSON
	mediaTypeSSE  = aichteeteapee.ContentTypeTextEventStream
	mediaTypeAny  = "*/*"

	invalidJSONBodyMessage     = "invalid JSON request body"
	eventTypeAndSummaryMessage = "type and summary are required"
	jobSignalRequiredMessage   = "signal is required"
	invalidJobStateMessage     = "unknown job state filter"
	invalidJobStreamMessage    = "unknown job output stream"
	invalidJobSignalMessage    = "unknown job signal"
	invalidSessionIDMessage    = "invalid session id"

	// errorLocationMarker opens the "[file:line in func]" suffix ctxerrors
	// appends to every wrapped error. Everything from here on is diagnostic
	// detail for logs, never for a response body.
	errorLocationMarker = " ["

	// invalidSessionIDParameterPrefix matches the generated parameter-binding
	// error text for a malformed X-Session-ID header value. oapi-codegen
	// writes this text at codegen time; it is not a spec-validator rejection,
	// so it never reaches validationErrorHandler.
	invalidSessionIDParameterPrefix = "Invalid format for parameter " +
		headerSessionID

	maximumRequestBodyBytes int64 = 1 << 20
	readHeaderTimeout             = 10 * time.Second
	shutdownTimeout               = 10 * time.Second
	networkTCP                    = "tcp"
)

type contextKey string

const requestIDContextKey contextKey = "request_id"

const streamContextKey contextKey = "stream"
