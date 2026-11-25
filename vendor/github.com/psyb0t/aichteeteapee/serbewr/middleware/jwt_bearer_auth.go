package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

const invalidBearerToken = "invalid_bearer_token"

// BearerPrincipal holds verified identity and OAuth claim values.
type BearerPrincipal struct {
	Issuer         string
	Subject        string
	Audience       string
	Scope          string
	ClientID       string
	OrganizationID string
	SessionID      string
	SubjectProfile string
}

type bearerPrincipalKey struct{}

// BearerPrincipalFromContext returns the identity set by JWTBearerAuth.
func BearerPrincipalFromContext(ctx context.Context) (BearerPrincipal, bool) {
	principal, ok := ctx.Value(bearerPrincipalKey{}).(BearerPrincipal)

	return principal, ok
}

// BearerTokenVerifier verifies a token for the configured route audience.
type BearerTokenVerifier func(
	ctx context.Context,
	token, audience string,
) (BearerPrincipal, error)

type jwtBearerAuthConfig struct {
	resourceMetadata string
}

// JWTBearerAuthOption configures JWTBearerAuth.
type JWTBearerAuthOption func(*jwtBearerAuthConfig)

// WithJWTBearerResourceMetadata sets the HTTPS URL in the 401 challenge.
// The caller serves that URL separately from protected routes.
func WithJWTBearerResourceMetadata(metadataURL string) JWTBearerAuthOption {
	return func(config *jwtBearerAuthConfig) {
		config.resourceMetadata = metadataURL
	}
}

// JWTBearerAuth verifies bearer tokens and exposes the verified identity.
// The application still decides which actions the identity may perform.
func JWTBearerAuth(
	verify BearerTokenVerifier,
	audience string,
	opts ...JWTBearerAuthOption,
) (Middleware, error) {
	if verify == nil || strings.TrimSpace(audience) == "" {
		return nil, ctxerrors.Wrap(
			aichteeteapee.ErrBadRequest,
			"JWT bearer auth requires a verifier and audience",
		)
	}

	config := jwtBearerAuthConfig{}
	for _, opt := range opts {
		opt(&config)
	}

	challenge := strings.TrimSpace(aichteeteapee.AuthSchemeBearer)

	if config.resourceMetadata != "" {
		if !validAuthURL(config.resourceMetadata) {
			return nil, ctxerrors.Wrap(
				aichteeteapee.ErrBadRequest,
				"resource metadata must be an HTTPS URL",
			)
		}

		challenge += " resource_metadata=" +
			strconv.Quote(config.resourceMetadata)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveJWTBearer(w, r, next, verify, audience, challenge)
		})
	}, nil
}

func serveJWTBearer(
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	verify BearerTokenVerifier,
	audience, challenge string,
) {
	principal, reason, err := verifyBearerRequest(r, verify, audience)
	if err != nil {
		rejectJWTBearer(w, r, challenge, reason)

		return
	}

	ctx := context.WithValue(r.Context(), bearerPrincipalKey{}, principal)
	ctx = context.WithValue(ctx, principalKey{}, Principal(principal))
	next.ServeHTTP(w, r.WithContext(ctx))
}

// NewBearerAuthenticator adapts a token verifier for RequireAuth. The
// verifier must check expiry and issuer and return ErrUnauthorized for
// invalid credentials. The adapter requires the exact configured audience.
func NewBearerAuthenticator(
	verify BearerTokenVerifier, audience string,
) (Authenticator, error) {
	if verify == nil || strings.TrimSpace(audience) == "" {
		return nil, ctxerrors.Wrap(
			aichteeteapee.ErrBadRequest,
			"bearer auth requires verifier and audience",
		)
	}

	return func(r *http.Request) (Principal, error) {
		principal, _, err := verifyBearerRequest(r, verify, audience)
		if err != nil {
			return Principal{}, ctxerrors.Wrap(
				err, "authenticate bearer request",
			)
		}

		return Principal(principal), nil
	}, nil
}

func verifyBearerRequest(
	r *http.Request, verify BearerTokenVerifier, audience string,
) (BearerPrincipal, string, error) {
	token, ok := bearerToken(r)
	authHeaders := r.Header.Values(aichteeteapee.HeaderNameAuthorization)

	if !ok || len(authHeaders) != 1 ||
		len(token) > aichteeteapee.DefaultJWTBearerMaxBytes ||
		strings.ContainsAny(token, " \t\r\n,") {
		return BearerPrincipal{}, "invalid_bearer_header",
			aichteeteapee.ErrUnauthorized
	}

	principal, err := verify(r.Context(), token, audience)
	if err != nil {
		return BearerPrincipal{}, invalidBearerToken,
			ctxerrors.Wrap(err, "verify bearer token")
	}

	if principal.Subject == "" || principal.Issuer == "" ||
		principal.Audience != audience {
		return BearerPrincipal{}, invalidBearerToken,
			aichteeteapee.ErrUnauthorized
	}

	return principal, "", nil
}

func rejectJWTBearer(
	w http.ResponseWriter,
	r *http.Request,
	challenge, reason string,
) {
	ctxscope.GetLogger(r.Context()).Warn(
		"JWT bearer authentication rejected",
		aichteeteapee.FieldReason, reason,
	)
	w.Header().Set(aichteeteapee.HeaderNameWWWAuthenticate, challenge)
	aichteeteapee.WriteJSON(
		w, http.StatusUnauthorized, aichteeteapee.ErrorResponseUnauthorized,
	)
}

func validAuthURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)

	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" &&
		parsed.User == nil && !strings.Contains(rawURL, "#") &&
		parsed.RawQuery == "" && !parsed.ForceQuery
}
