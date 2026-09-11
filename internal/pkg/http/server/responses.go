package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxscope"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

func requestID(ctx context.Context) uuid.UUID {
	value, found := ctx.Value(aichteeteapee.ContextKeyRequestID).(string)
	if found {
		parsed, err := uuid.Parse(value)
		if err == nil {
			return parsed
		}
	}

	return uuid.New()
}

func (s *Server) handleRequestError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	code := aichteeteapee.ErrorCodeValidationFailed
	message := validationFailureMessage

	if isInvalidSessionIDError(err) {
		code = ErrorCodeInvalidSessionID
		message = invalidSessionIDMessage
	}

	logHTTPRejection(r.Context(), http.StatusBadRequest, code, err)
	writeAPIError(w, http.StatusBadRequest, code, message)
}

func (s *Server) handleResponseError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	logHTTPRejection(
		r.Context(),
		http.StatusInternalServerError,
		aichteeteapee.ErrorCodeInternalServerError,
		err,
	)
	writeAPIError(
		w,
		http.StatusInternalServerError,
		aichteeteapee.ErrorCodeInternalServerError,
		internalServerErrorMessage,
	)
}

func logHTTPRejection(
	ctx context.Context,
	status int,
	code aichteeteapee.ErrorCode,
	err error,
) {
	logger := ctxscope.GetLogger(ctx)
	if status >= http.StatusInternalServerError {
		logger.Error(
			httpRequestFailedLogMessage,
			"status", status,
			"code", code,
			"err", err,
		)

		return
	}

	logger.Warn(
		httpRequestRejectedLogMessage,
		"status", status,
		"code", code,
		"err", err,
	)
}

func isInvalidSessionIDError(err error) bool {
	invalidFormat := &api.InvalidParamFormatError{}
	if errors.As(err, &invalidFormat) {
		return invalidFormat.ParamName == headerSessionID
	}

	return false
}

func validationError(message string) api.Error {
	return api.Error{
		Code:    aichteeteapee.ErrorCodeValidationFailed,
		Message: message,
	}
}

// clientMessage strips the source location ctxerrors appends before an error
// reaches a client.
//
// ctxerrors formats as "message: wrapped [file:line in func]", and nested
// wraps append their own marker, so every marker sits after the readable chain
// and the first " [" is where the text stops being safe to publish. Sending
// Error() verbatim puts absolute source paths, line numbers, and internal
// function names in a response body any caller can read.
func clientMessage(err error) string {
	if err == nil {
		return ""
	}

	message := err.Error()
	if index := strings.Index(message, errorLocationMarker); index >= 0 {
		message = message[:index]
	}

	return strings.TrimSpace(message)
}

func notFoundError() api.Error {
	return api.Error{
		Code:    aichteeteapee.ErrorCodeNotFound,
		Message: "session not found",
	}
}

// sessionNotFoundError reports a well-formed X-Session-ID that names no
// durable session. Only used where a session ID is the sole identifier an
// operation resolves, so an ErrNotFound there can only mean this.
func sessionNotFoundError() api.Error {
	return api.Error{
		Code:    ErrorCodeSessionNotFound,
		Message: "session not found",
	}
}

func sessionBusyError(message string) api.Error {
	return api.Error{Code: ErrorCodeSessionBusy, Message: message}
}

func turnCancelledError() api.Error {
	return api.Error{
		Code:    ErrorCodeTurnCancelled,
		Message: "turn was cancelled",
	}
}

func userMessageQueueFullError() api.Error {
	return api.Error{
		Code:    ErrorCodeUserMessageQueueFull,
		Message: "active turn user message queue is full",
	}
}
