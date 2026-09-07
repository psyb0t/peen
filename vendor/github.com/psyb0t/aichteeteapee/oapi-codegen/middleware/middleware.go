package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/oapi-codegen/echo-middleware"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
)

func OapiValidatorMiddleware(spec *openapi3.T) echo.MiddlewareFunc {
	return OapiValidatorMiddlewareWithOptions(spec, nil)
}

func OapiValidatorMiddlewareWithOptions(
	spec *openapi3.T,
	opts *echomiddleware.Options,
) echo.MiddlewareFunc {
	if opts == nil {
		opts = &echomiddleware.Options{}
	}

	if opts.ErrorHandler == nil {
		opts.ErrorHandler = errorHandler
	}

	if opts.MultiErrorHandler == nil {
		opts.MultiErrorHandler = multiErrorHandler
	}

	return echomiddleware.OapiRequestValidatorWithOptions(spec, opts)
}

func errorHandler(
	c echo.Context,
	err *echo.HTTPError,
) error {
	slog.Error("request validation failed",
		"message", err.Message,
		"status_code", err.Code,
	)

	resp := aichteeteapee.ErrorResponse{
		Code:    aichteeteapee.ErrorCodeFromHTTPStatus(err.Code),
		Message: fmt.Sprintf("%v", err.Message),
	}

	if jsonErr := c.JSON(err.Code, resp); jsonErr != nil {
		return ctxerrors.Wrap(jsonErr, "failed to send error response")
	}

	return nil
}

func multiErrorHandler(errs openapi3.MultiError) *echo.HTTPError {
	errMsgs := make([]string, 0, len(errs))
	for _, err := range errs {
		errMsgs = append(errMsgs, err.Error())
	}

	slog.Error(
		"multiple validation errors",
		"errors", errMsgs,
	)

	msg := fmt.Sprintf(
		"Validation failed: %s",
		strings.Join(errMsgs, "; "),
	)

	return echo.NewHTTPError(http.StatusBadRequest, msg)
}
