package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/oapi-codegen/echo-middleware"
	"github.com/psyb0t/aichteeteapee"
	oapimiddleware "github.com/psyb0t/aichteeteapee/oapi-codegen/middleware"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

const validationErrorJoiner = "; "

// specValidator validates every request against the generated OpenAPI
// document before it reaches a handler.
//
// This replaces hand-written shape checks in each handler. Those checks kept
// being forgotten: an empty POST body and unknown query enum values both
// reached handlers and produced misleading successful responses until tests
// caught them one endpoint at a time. The spec already declares required
// fields, enums, formats, and additionalProperties, so it is the single place
// that knowledge belongs.
//
// It deliberately does NOT do authentication. The spec declares security as
// `bearerAuth` OR `{}`, meaning optional, because Peen's auth is decided at
// runtime by whether PEEN_API_TOKEN is set. A spec cannot express "required
// only when the deployment configured a token", so the authenticate
// middleware keeps that job.
//
// The document is used exactly as generated, servers entry included. That
// entry is `/v1` and the routes mount at the same base URL, which is what lets
// the validator's router strip the prefix and resolve an operation.
func specValidator() (echo.MiddlewareFunc, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load the generated OpenAPI document")
	}

	return oapimiddleware.OapiValidatorMiddlewareWithOptions(
		spec,
		&echomiddleware.Options{
			ErrorHandler:      validationErrorHandler,
			MultiErrorHandler: validationMultiErrorHandler,
			Options: openapi3filter.Options{
				// Authentication is the authenticate middleware's job, so
				// accept every security requirement here rather than
				// rejecting requests the runtime is meant to allow.
				AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			},
		},
	), nil
}

// validationErrorHandler keeps the documented error envelope. The library
// maps a 400 to BAD_REQUEST, but Peen's contract already says a malformed
// request shape is VALIDATION_FAILED, and changing that for only the inputs
// the validator happens to catch would be worse than either choice alone.
func validationErrorHandler(c echo.Context, err *echo.HTTPError) error {
	ctxscope.GetLogger(c.Request().Context()).Debug(
		"request rejected by spec validation",
		"status", err.Code,
	)

	response := api.Error{
		Code:    validationErrorCode(err.Code),
		Message: fmt.Sprintf("%v", err.Message),
	}

	if jsonErr := c.JSON(err.Code, response); jsonErr != nil {
		return ctxerrors.Wrap(jsonErr, "write validation error response")
	}

	return nil
}

func validationMultiErrorHandler(errs openapi3.MultiError) *echo.HTTPError {
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		messages = append(messages, err.Error())
	}

	return echo.NewHTTPError(
		http.StatusBadRequest,
		strings.Join(messages, validationErrorJoiner),
	)
}

func validationErrorCode(status int) aichteeteapee.ErrorCode {
	if status == http.StatusBadRequest {
		return aichteeteapee.ErrorCodeValidationFailed
	}

	return aichteeteapee.ErrorCodeFromHTTPStatus(status)
}
