package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxscope"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

func (s *Server) recoverPanic(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) (handlerErr error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				ctxscope.GetLogger(c.Request().Context()).Error(
					"HTTP handler panicked",
					"panic", recovered,
					"stack", string(debug.Stack()),
				)

				handlerErr = echo.NewHTTPError(http.StatusInternalServerError)
			}
		}()

		return next(c)
	}
}

func (s *Server) requestContext(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		requestID := uuid.New()

		providedRequestID := c.Request().Header.Get(headerRequestID)
		if providedRequestID != "" {
			parsedRequestID, err := uuid.Parse(providedRequestID)
			if err == nil {
				requestID = parsedRequestID
			}
		}

		ctx := ctxscope.Set(
			c.Request().Context(),
			ctxscope.Attr("request_id", requestID.String()),
		)
		ctx = context.WithValue(ctx, requestIDContextKey, requestID)
		c.SetRequest(c.Request().WithContext(ctx))
		c.Response().Header().Set(headerRequestID, requestID.String())

		return next(c)
	}
}

func (s *Server) accessLog(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		startedAt := time.Now()

		ctxscope.GetLogger(c.Request().Context()).Info(
			"HTTP request started",
			"method", c.Request().Method,
			"path", c.Path(),
		)

		err := next(c)
		if err != nil {
			c.Error(err)

			ctxscope.GetLogger(c.Request().Context()).Info(
				"HTTP request completed",
				"method", c.Request().Method,
				"path", c.Path(),
				"status", c.Response().Status,
				"duration_ms", time.Since(startedAt).Milliseconds(),
				"err", err,
			)

			return nil
		}

		ctxscope.GetLogger(c.Request().Context()).Info(
			"HTTP request completed",
			"method", c.Request().Method,
			"path", c.Path(),
			"status", c.Response().Status,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)

		return nil
	}
}

func (s *Server) authenticate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if s.deps.APIToken == "" {
			return next(c)
		}

		if !isValidBearerToken(
			c.Request().Header.Get(headerAuthorization),
			s.deps.APIToken,
		) {
			return c.JSON(http.StatusUnauthorized, api.Error{
				Code:    aichteeteapee.ErrorCodeUnauthorized,
				Message: "invalid bearer token",
			})
		}

		return next(c)
	}
}

func isValidBearerToken(authorization string, expectedToken string) bool {
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return false
	}

	return subtle.ConstantTimeCompare(
		[]byte(token),
		[]byte(expectedToken),
	) == 1
}

func (s *Server) limitBody(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		request := c.Request()
		request.Body = http.MaxBytesReader(
			c.Response(),
			request.Body,
			maximumRequestBodyBytes,
		)
		c.SetRequest(request)

		return next(c)
	}
}

func (s *Server) negotiateResponse(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		stream, accepted := acceptedRepresentation(
			c.Request().Method,
			c.Request().Header.Get(headerAccept),
		)
		if !accepted {
			return c.JSON(http.StatusNotAcceptable, api.Error{
				Code:    aichteeteapee.ErrorCodeBadRequest,
				Message: "unsupported response representation",
			})
		}

		ctx := context.WithValue(
			c.Request().Context(),
			streamContextKey,
			stream,
		)
		c.SetRequest(c.Request().WithContext(ctx))

		return next(c)
	}
}

func acceptedRepresentation(method string, accept string) (bool, bool) {
	if accept == "" {
		return false, true
	}

	for value := range strings.SplitSeq(accept, ",") {
		mediaType, _, _ := strings.Cut(strings.TrimSpace(value), ";")
		switch mediaType {
		case mediaTypeAny, mediaTypeJSON:
			return false, true
		case mediaTypeSSE:
			if method == http.MethodPost {
				return true, true
			}
		}
	}

	return false, false
}
