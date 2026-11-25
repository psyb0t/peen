package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

// Principal identifies a verified subject. Issuer and other claims are
// populated when supplied by the authenticator.
type Principal BearerPrincipal

type principalKey struct{}

// PrincipalFromContext returns the verified request identity.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)

	return principal, ok
}

// Authenticator verifies the credentials accepted on a route. Return a
// nonempty Subject on success, ErrUnauthorized for invalid or missing
// credentials, or ErrForbidden for a disallowed request. Other errors fail
// closed with a server error.
type Authenticator func(*http.Request) (Principal, error)

type authConfig struct {
	loginPath string
	challenge func(error) string
}

// AuthOption configures RequireAuth.
type AuthOption func(*authConfig)

// WithAuthLoginRedirect sends unauthenticated GET and HEAD requests to a fixed
// same-origin login path. Other methods still receive a JSON 401.
func WithAuthLoginRedirect(path string) AuthOption {
	return func(config *authConfig) {
		config.loginPath = path
	}
}

// WithAuthChallenge supplies WWW-Authenticate for 401 and 403 responses.
// Return an empty string to omit the header.
func WithAuthChallenge(challenge func(error) string) AuthOption {
	return func(config *authConfig) {
		config.challenge = challenge
	}
}

// RequireAuth protects routes with the credentials accepted by authenticate.
// The verified principal is available through PrincipalFromContext.
func RequireAuth(
	authenticate Authenticator,
	opts ...AuthOption,
) (Middleware, error) {
	config := authConfig{}
	for _, opt := range opts {
		opt(&config)
	}

	if authenticate == nil || !validLoginPath(config.loginPath) {
		return nil, ctxerrors.Wrap(
			aichteeteapee.ErrBadRequest, "invalid auth configuration",
		)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := authenticate(r)
			if err == nil && strings.TrimSpace(principal.Subject) == "" {
				err = aichteeteapee.ErrUnauthorized
			}

			if err != nil {
				config.reject(w, r, err)

				return
			}

			ctx := context.WithValue(r.Context(), principalKey{}, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}, nil
}

func validLoginPath(path string) bool {
	if path == "" {
		return true
	}

	parsed, err := url.Parse(path)

	return err == nil && parsed.EscapedPath() == path && parsed.Path == path &&
		strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") &&
		!strings.Contains(path, `\`)
}

func (config authConfig) reject(
	w http.ResponseWriter, r *http.Request, err error,
) {
	w.Header().Set(aichteeteapee.HeaderNameCacheControl, "no-store")

	switch {
	case errors.Is(err, aichteeteapee.ErrForbidden):
		config.setChallenge(w, err)
		aichteeteapee.WriteJSON(
			w, http.StatusForbidden, aichteeteapee.ErrorResponseForbidden,
		)
	case errors.Is(err, aichteeteapee.ErrUnauthorized):
		if config.loginPath != "" &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) {
			http.Redirect(w, r, config.loginPath, http.StatusSeeOther)

			return
		}

		config.setChallenge(w, err)
		aichteeteapee.WriteJSON(
			w, http.StatusUnauthorized, aichteeteapee.ErrorResponseUnauthorized,
		)
	default:
		ctxscope.GetLogger(r.Context()).Error(
			"authenticate request",
			aichteeteapee.FieldReason, "authenticator_error",
		)
		aichteeteapee.WriteJSON(
			w, http.StatusInternalServerError,
			aichteeteapee.ErrorResponseInternalServerError,
		)
	}
}

func (config authConfig) setChallenge(w http.ResponseWriter, err error) {
	if config.challenge == nil {
		return
	}

	if challenge := config.challenge(err); challenge != "" {
		w.Header().Set(aichteeteapee.HeaderNameWWWAuthenticate, challenge)
	}
}
