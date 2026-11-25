package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
)

type jwksConfig struct {
	transport  http.RoundTripper
	algorithms []jose.SignatureAlgorithm
}

// JWKSOption configures a JWKSVerifier.
type JWKSOption func(*jwksConfig)

// WithJWKSTransport configures the trusted transport for fetching signing keys.
func WithJWKSTransport(transport http.RoundTripper) JWKSOption {
	return func(config *jwksConfig) {
		config.transport = transport
	}
}

// WithJWKSAlgorithms replaces the default RS256 signature allowlist.
func WithJWKSAlgorithms(algorithms ...jose.SignatureAlgorithm) JWKSOption {
	return func(config *jwksConfig) {
		config.algorithms = slices.Clone(algorithms)
	}
}

// JWKSVerifier validates JWTs for one trusted issuer and caches its keys.
// Construct it with NewJWKSVerifier and share it across routes for that issuer.
type JWKSVerifier struct {
	issuer     string
	url        string
	client     *http.Client
	algorithms []jose.SignatureAlgorithm
	refresh    chan struct{}
	mu         sync.RWMutex
	keys       jose.JSONWebKeySet
	expires    time.Time
	nextFetch  time.Time
}

// NewJWKSVerifier uses the configured HTTPS issuer and JWKS URL. Requests
// fetch keys on demand and never follow redirects from the JWKS endpoint.
func NewJWKSVerifier(
	issuer, jwksURL string,
	opts ...JWKSOption,
) (*JWKSVerifier, error) {
	if !validAuthURL(issuer) || !validAuthURL(jwksURL) {
		return nil, ctxerrors.Wrap(
			aichteeteapee.ErrBadRequest, "issuer and JWKS require HTTPS URLs",
		)
	}

	config := jwksConfig{algorithms: []jose.SignatureAlgorithm{jose.RS256}}
	for _, opt := range opts {
		opt(&config)
	}

	if len(config.algorithms) == 0 ||
		slices.ContainsFunc(config.algorithms, invalidJWKAlgorithm) {
		return nil, ctxerrors.Wrap(
			aichteeteapee.ErrBadRequest, "JWKS requires asymmetric algorithms",
		)
	}

	return &JWKSVerifier{
		issuer:     issuer,
		url:        jwksURL,
		algorithms: config.algorithms,
		refresh:    make(chan struct{}, 1),
		client: &http.Client{
			Transport: config.transport,
			Timeout:   aichteeteapee.DefaultJWKSFetchTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func invalidJWKAlgorithm(algorithm jose.SignatureAlgorithm) bool {
	allowed := [...]jose.SignatureAlgorithm{
		jose.RS256, jose.RS384, jose.RS512, jose.PS256, jose.PS384, jose.PS512,
		jose.ES256, jose.ES384, jose.ES512, jose.EdDSA,
	}

	return !slices.Contains(allowed[:], algorithm)
}

//nolint:tagliatelle // OAuth claim names follow the issuer's wire format.
type connectClaims struct {
	jwt.Claims

	Scope          string `json:"scope"`
	ClientID       string `json:"client_id"`
	OrganizationID string `json:"org_id"`
	SessionID      string `json:"sid"`
	SubjectProfile string `json:"sub_profile"`
}

// Verify validates a signature and requires a subject, expiry and exactly
// one audience matching the route. It never uses key URLs from the token.
func (v *JWKSVerifier) Verify(
	ctx context.Context,
	rawToken, audience string,
) (BearerPrincipal, error) {
	token, err := jwt.ParseSigned(rawToken, v.algorithms)
	if err != nil || len(token.Headers) != 1 || token.Headers[0].KeyID == "" {
		return BearerPrincipal{}, ctxerrors.Wrap(
			aichteeteapee.ErrUnauthorized, "invalid JWT header",
		)
	}

	key, err := v.signingKey(ctx, token.Headers[0])
	if err != nil {
		return BearerPrincipal{}, ctxerrors.Wrap(err, "resolve JWT signing key")
	}

	claims := connectClaims{}
	if err := token.Claims(key.Key, &claims); err != nil {
		return BearerPrincipal{}, ctxerrors.Wrap(
			aichteeteapee.ErrUnauthorized, "invalid JWT signature or claims",
		)
	}

	return v.principal(claims, audience)
}

func (v *JWKSVerifier) principal(
	claims connectClaims,
	audience string,
) (BearerPrincipal, error) {
	now := time.Now()
	if claims.Subject == "" || claims.Expiry == nil ||
		!now.Before(claims.Expiry.Time()) || len(claims.Audience) != 1 ||
		claims.Audience[0] != audience || audience == "" {
		return BearerPrincipal{}, ctxerrors.Wrap(
			aichteeteapee.ErrUnauthorized,
			"invalid JWT subject, expiry or audience",
		)
	}

	if err := claims.ValidateWithLeeway(jwt.Expected{
		Issuer: v.issuer,
		Time:   now,
	}, 0); err != nil {
		return BearerPrincipal{}, ctxerrors.Wrap(
			aichteeteapee.ErrUnauthorized, "invalid JWT issuer or time claims",
		)
	}

	return BearerPrincipal{
		Issuer:         claims.Issuer,
		Subject:        claims.Subject,
		Audience:       audience,
		Scope:          claims.Scope,
		ClientID:       claims.ClientID,
		OrganizationID: claims.OrganizationID,
		SessionID:      claims.SessionID,
		SubjectProfile: claims.SubjectProfile,
	}, nil
}

func (v *JWKSVerifier) signingKey(
	ctx context.Context,
	header jose.Header,
) (jose.JSONWebKey, error) {
	if key, ok := v.cachedKey(header); ok {
		return key, nil
	}

	select {
	case v.refresh <- struct{}{}:
		defer func() { <-v.refresh }()
	case <-ctx.Done():
		return jose.JSONWebKey{}, ctxerrors.Wrap(
			ctx.Err(), "wait for JWKS refresh",
		)
	}

	if key, ok := v.cachedKey(header); ok {
		return key, nil
	}

	if time.Now().Before(v.nextFetch) {
		return jose.JSONWebKey{}, ctxerrors.Wrap(
			aichteeteapee.ErrUnauthorized, "signing key unavailable",
		)
	}

	keys, err := v.fetchKeys(ctx)
	v.nextFetch = time.Now().Add(aichteeteapee.DefaultJWKSRefreshInterval)

	if err != nil {
		return jose.JSONWebKey{}, ctxerrors.Wrap(err, "refresh JWKS")
	}

	v.mu.Lock()
	v.keys = keys
	v.expires = time.Now().Add(aichteeteapee.DefaultJWKSCacheTTL)
	v.mu.Unlock()

	key, ok := v.cachedKey(header)
	if !ok {
		return jose.JSONWebKey{}, ctxerrors.Wrap(
			aichteeteapee.ErrUnauthorized, "signing key unavailable",
		)
	}

	return key, nil
}

func (v *JWKSVerifier) cachedKey(header jose.Header) (jose.JSONWebKey, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if !time.Now().Before(v.expires) {
		return jose.JSONWebKey{}, false
	}

	keys := v.keys.Key(header.KeyID)
	if len(keys) != 1 {
		return jose.JSONWebKey{}, false
	}

	key := keys[0]
	valid := key.Valid() && key.IsPublic() &&
		(key.Use == "" || key.Use == "sig") &&
		(key.Algorithm == "" || key.Algorithm == header.Algorithm)

	return key, valid
}

func (v *JWKSVerifier) fetchKeys(
	ctx context.Context,
) (jose.JSONWebKeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return jose.JSONWebKeySet{}, ctxerrors.Wrap(
			aichteeteapee.ErrAPIError, "create JWKS request",
		)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return jose.JSONWebKeySet{}, ctxerrors.Wrap(
			aichteeteapee.ErrAPIError, "fetch JWKS",
		)
	}

	defer func() {
		// The response is read-only, with no state to flush on close.
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return jose.JSONWebKeySet{}, ctxerrors.Wrap(
			aichteeteapee.ErrAPIError, "unexpected JWKS status",
		)
	}

	data, err := io.ReadAll(io.LimitReader(
		resp.Body, aichteeteapee.DefaultJWKSMaxBytes+1,
	))
	if err != nil || len(data) > aichteeteapee.DefaultJWKSMaxBytes {
		return jose.JSONWebKeySet{}, ctxerrors.Wrap(
			aichteeteapee.ErrAPIError, "invalid JWKS response size or body",
		)
	}

	var keys jose.JSONWebKeySet
	if err := json.Unmarshal(data, &keys); err != nil || len(keys.Keys) == 0 {
		return jose.JSONWebKeySet{}, ctxerrors.Wrap(
			aichteeteapee.ErrAPIError, "invalid or empty JWKS",
		)
	}

	return keys, nil
}
