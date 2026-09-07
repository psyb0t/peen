package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

func requestID(ctx context.Context) uuid.UUID {
	value, found := ctx.Value(requestIDContextKey).(uuid.UUID)
	if found {
		return value
	}

	return uuid.New()
}

func (s *Server) handleHTTPError(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	status := http.StatusInternalServerError
	code := aichteeteapee.ErrorCodeInternalServerError
	message := "internal server error"

	httpError := &echo.HTTPError{}
	if errors.As(err, &httpError) {
		status = httpError.Code

		code = aichteeteapee.ErrorCodeFromHTTPStatus(status)
		if status == http.StatusBadRequest {
			code = aichteeteapee.ErrorCodeValidationFailed
			message = "invalid request"

			if isInvalidSessionIDParameter(httpError.Message) {
				code = ErrorCodeInvalidSessionID
				message = invalidSessionIDMessage
			}
		}
	}

	ctxscope.GetLogger(c.Request().Context()).Warn(
		"HTTP request rejected",
		"status", status,
		"code", code,
		"err", err,
	)

	errorResponse := api.Error{Code: code, Message: message}
	if writeErr := c.JSON(status, errorResponse); writeErr != nil {
		ctxscope.GetLogger(c.Request().Context()).Debug(
			"HTTP error response write failed",
			"err", writeErr,
		)
	}
}

//nolint:ireturn // Generated strict handler response interface.
func mapSendMessageError(err error) (api.SendMessageResponseObject, bool) {
	switch {
	case errors.Is(err, commerr.ErrNotFound):
		return api.SendMessage404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				sessionNotFoundError(),
			),
		}, true
	case errors.Is(err, commerr.ErrValidationFailed),
		errors.Is(err, commerr.ErrRequiredFieldNotSet):
		return api.SendMessage400JSONResponse{
			ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
				validationError(clientMessage(err)),
			),
		}, true
	case errors.Is(err, commerr.ErrCancelled):
		return api.SendMessage409JSONResponse{
			ErrorConflictJSONResponse: api.ErrorConflictJSONResponse(
				turnCancelledError(),
			),
		}, true
	case errors.Is(err, commerr.ErrConflict):
		return api.SendMessage409JSONResponse{
			ErrorConflictJSONResponse: api.ErrorConflictJSONResponse(
				sessionBusyError("session already has an active turn"),
			),
		}, true
	default:
		return nil, false
	}
}

// isInvalidSessionIDParameter reports whether a parameter-binding error came
// from a malformed X-Session-ID header. oapi-codegen's generated wrapper
// produces this exact message text before specValidator ever sees the
// request, so this is the only place that can recognize it.
func isInvalidSessionIDParameter(rawMessage any) bool {
	message, ok := rawMessage.(string)
	if !ok {
		return false
	}

	return strings.HasPrefix(message, invalidSessionIDParameterPrefix)
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
