package openaicompat

import (
	"context"
	"errors"
	"net/http"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func invalidField(field string) responseError {
	return responseError{
		Message: "Invalid or unsupported field: " + field + ".",
		Type:    errorTypeRequest,
		Code:    codeInvalidRequest,
	}
}

func runnerError(err error) (int, responseError) {
	var conflict *session.AgentSpaceConflictError
	if errors.As(err, &conflict) {
		return http.StatusConflict, responseError{
			Message: conflict.Error(),
			Type:    errorTypeRequest, Code: codeAgentSpaceConflict,
		}
	}

	switch {
	case errors.Is(err, commerr.ErrPermissionDenied):
		return http.StatusForbidden, responseError{
			Message: messagePermissionDenied,
			Type:    errorTypeRequest, Code: codePermissionDenied,
		}
	case errors.Is(err, commerr.ErrNotFound):
		return http.StatusNotFound, responseError{
			Message: messageNotFound,
			Type:    errorTypeRequest, Code: codeNotFound,
		}
	case errors.Is(err, session.ErrSessionBusy),
		errors.Is(err, commerr.ErrLockHeld),
		errors.Is(err, commerr.ErrConflict):
		return http.StatusConflict, responseError{
			Message: messageSessionBusy,
			Type:    errorTypeRequest, Code: codeSessionBusy,
		}
	case errors.Is(err, agent.ErrTurnTimeout),
		errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, responseError{
			Message: messageTurnTimeout,
			Type:    errorTypeServer, Code: codeTurnTimeout,
		}
	case errors.Is(err, commerr.ErrValidationFailed),
		errors.Is(err, agent.ErrOutputSchemaInvalid):
		return http.StatusBadRequest, responseError{
			Message: messageInvalidRequest,
			Type:    errorTypeRequest, Code: codeInvalidRequest,
		}
	default:
		return http.StatusInternalServerError, responseError{
			Message: messageTurnFailed,
			Type:    errorTypeServer, Code: codeTurnFailed,
		}
	}
}
