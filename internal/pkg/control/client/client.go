// Package client is the local control client behind Peen's CLI commands.
//
// It speaks only the control HTTP API. It never opens SQLite, never builds an
// agent runtime, and never resolves a workspace itself, so a command cannot
// disagree with the running controller about durable state.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/http/api"
)

const (
	defaultRequestTimeout = 30 * time.Second

	readyPath         = "/ready"
	sessionsPath      = "/v1/sessions"
	openSessionPath   = "/v1/sessions/open"
	cancelSessionPath = "/v1/session/cancel"

	// headerSessionID selects the durable session for a session-scoped
	// endpoint. It is Peen's own header and has no shared-library equivalent.
	headerSessionID = "X-Session-ID"

	bearerScheme = "Bearer "

	// maxErrorBodyBytes bounds what a failed response contributes to an error
	// message, because the body is remote input.
	maxErrorBodyBytes = 4096
)

// Options configures one control client.
type Options struct {
	// ListenAddress is the controller's configured HTTP address. A value
	// without a host, such as ":8080", is resolved to loopback.
	ListenAddress string

	// Token is the controller's bearer token. Empty matches a deployment that
	// configured none.
	Token string

	// HTTPClient overrides the transport. Tests supply their own.
	HTTPClient *http.Client
}

// Client calls one local controller.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New builds a client for the configured controller endpoint.
func New(options Options) (*Client, error) {
	if options.ListenAddress == "" {
		return nil, ctxerrors.Wrap(
			commerr.ErrRequiredFieldNotSet,
			"control listen address",
		)
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}

	return &Client{
		baseURL:    "http://" + control.DialableAddress(options.ListenAddress),
		token:      options.Token,
		httpClient: httpClient,
	}, nil
}

// Reachable reports whether the controller answers its readiness probe. It is
// the discovery step: a false result means no controller is listening yet.
func (c *Client) Reachable(ctx context.Context) bool {
	response, err := c.do(ctx, http.MethodGet, readyPath, nil)
	if err != nil {
		return false
	}

	defer c.closeBody(ctx, response)

	return response.StatusCode == http.StatusOK
}

// OpenSession opens or resumes the session for one workspace.
func (c *Client) OpenSession(
	ctx context.Context,
	workspace string,
) (api.OpenedSession, error) {
	body, err := json.Marshal(api.OpenSessionRequest{Workspace: workspace})
	if err != nil {
		return api.OpenedSession{}, ctxerrors.Wrap(
			err,
			"encode open session request",
		)
	}

	opened := api.OpenedSession{}
	if err := c.callJSON(
		ctx,
		http.MethodPost,
		openSessionPath,
		body,
		&opened,
	); err != nil {
		return api.OpenedSession{}, err
	}

	return opened, nil
}

// ListSessions reads one page of the controller's durable sessions.
func (c *Client) ListSessions(ctx context.Context) (api.SessionPage, error) {
	page := api.SessionPage{}
	if err := c.callJSON(
		ctx,
		http.MethodGet,
		sessionsPath,
		nil,
		&page,
	); err != nil {
		return api.SessionPage{}, err
	}

	return page, nil
}

// CancelSession asks the controller to stop the session's active turn. It
// reports whether a turn was running: a session with nothing in flight is not
// an error, it simply has nothing to cancel.
func (c *Client) CancelSession(
	ctx context.Context,
	sessionID uuid.UUID,
) (api.CancelResponse, error) {
	result := api.CancelResponse{}
	if err := c.callSessionJSON(
		ctx,
		http.MethodPost,
		cancelSessionPath,
		sessionID,
		http.StatusAccepted,
		&result,
	); err != nil {
		return api.CancelResponse{}, err
	}

	return result, nil
}

// callSessionJSON runs one session-scoped request and decodes its response.
func (c *Client) callSessionJSON(
	ctx context.Context,
	method string,
	path string,
	sessionID uuid.UUID,
	wantStatus int,
	target any,
) error {
	response, err := c.doWithSession(ctx, method, path, nil, sessionID)
	if err != nil {
		return err
	}

	defer c.closeBody(ctx, response)

	if response.StatusCode != wantStatus {
		return c.responseError(response)
	}

	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return ctxerrors.Wrap(err, "decode control response")
	}

	return nil
}

// callJSON runs one request and decodes a successful JSON response.
func (c *Client) callJSON(
	ctx context.Context,
	method string,
	path string,
	body []byte,
	target any,
) error {
	response, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}

	defer c.closeBody(ctx, response)

	if response.StatusCode != http.StatusOK {
		return c.responseError(response)
	}

	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return ctxerrors.Wrap(err, "decode control response")
	}

	return nil
}

func (c *Client) do(
	ctx context.Context,
	method string,
	path string,
	body []byte,
) (*http.Response, error) {
	return c.doWithSession(ctx, method, path, body, uuid.Nil)
}

func (c *Client) doWithSession(
	ctx context.Context,
	method string,
	path string,
	body []byte,
	sessionID uuid.UUID,
) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(
		ctx,
		method,
		c.baseURL+path,
		reader,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create control request")
	}

	if body != nil {
		request.Header.Set(
			aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeJSON,
		)
	}

	if sessionID != uuid.Nil {
		request.Header.Set(headerSessionID, sessionID.String())
	}

	if c.token != "" {
		request.Header.Set(
			aichteeteapee.HeaderNameAuthorization,
			bearerScheme+c.token,
		)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "call the control API")
	}

	return response, nil
}

// responseError turns a non-200 into the API's own error envelope when the
// body carries one, so a command prints the controller's stable code.
func (c *Client) responseError(response *http.Response) error {
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	if err != nil {
		return ctxerrors.Wrapf(
			commerr.ErrFetchFailed,
			"control API returned %d",
			response.StatusCode,
		)
	}

	envelope := api.Error{}
	if err := json.Unmarshal(payload, &envelope); err != nil ||
		envelope.Code == "" {
		return ctxerrors.Wrapf(
			commerr.ErrFetchFailed,
			"control API returned %d",
			response.StatusCode,
		)
	}

	return ctxerrors.Wrapf(
		commerr.ErrFetchFailed,
		"control API returned %d %s: %s",
		response.StatusCode,
		envelope.Code,
		envelope.Message,
	)
}

func (c *Client) closeBody(ctx context.Context, response *http.Response) {
	if err := response.Body.Close(); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"closing the control response body failed",
			"err", err,
		)
	}
}
