package server

import (
	"net/http"

	"github.com/getkin/kin-openapi/openapi3filter"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// specValidator validates every declared API request before it reaches the
// generated handler. Unknown paths pass through so ServeMux retains its normal
// 404 and 405 behavior.
func specValidator() (middleware.Middleware, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load generated OpenAPI document")
	}

	router, err := legacyrouter.NewRouter(spec)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build OpenAPI request router")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, pathParams, routeErr := router.FindRoute(r)
			if routeErr != nil {
				next.ServeHTTP(w, r)

				return
			}

			validationErr := openapi3filter.ValidateRequest(
				r.Context(),
				&openapi3filter.RequestValidationInput{
					Request:    r,
					PathParams: pathParams,
					Route:      route,
					Options: &openapi3filter.Options{
						//nolint:lll // The kin-openapi callback is an upstream API name.
						AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
					},
				},
			)
			if validationErr == nil {
				next.ServeHTTP(w, r)

				return
			}

			ctxscope.GetLogger(r.Context()).Debug(
				"request rejected by spec validation",
				"status", http.StatusBadRequest,
				"err", validationErr,
			)
			writeAPIError(
				w,
				http.StatusBadRequest,
				aichteeteapee.ErrorCodeValidationFailed,
				validationFailureMessage,
			)
		})
	}, nil
}
