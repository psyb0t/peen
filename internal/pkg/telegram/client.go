package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/psyb0t/aichteeteapee"
)

func New(token string) (*Client, error) {
	return newClient(token, apiBaseURL, &http.Client{})
}

// Endpoint injection is private and exists solely for loopback HTTP tests.
func newClient(token, endpoint string, client *http.Client) (*Client, error) {
	if !validToken(token) || client == nil || !validEndpoint(endpoint) {
		return nil, invalidInput()
	}

	copied := *client
	copied.Timeout = clientTimeout
	copied.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Client{
		token: token, baseURL: strings.TrimRight(endpoint, "/"),
		http: &copied,
	}, nil
}

func validEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}

	return (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" &&
		parsed.Fragment == "" && (parsed.Path == "" || parsed.Path == "/")
}

func (c *Client) call(
	ctx context.Context, method string, body io.Reader,
	contentType string, timeout time.Duration,
) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	data, err := c.exchange(ctx, http.MethodPost,
		c.baseURL+"/bot"+c.token+"/"+method,
		body, contentType, maxJSONBytes)
	if err != nil {
		return nil, err
	}

	var response apiResponse

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, invalidResponse()
	}

	if !response.OK || len(response.Result) == 0 {
		return nil, requestFailure(ctx)
	}

	return response.Result, nil
}

func (c *Client) exchange(
	ctx context.Context, method, endpoint string, body io.Reader,
	contentType string, maximum int64,
) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, requestFailure(ctx)
	}

	if contentType != "" {
		request.Header.Set(aichteeteapee.HeaderNameContentType, contentType)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return nil, requestFailure(ctx)
	}

	data, readErr := io.ReadAll(io.LimitReader(response.Body, maximum+1))

	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, requestFailure(ctx)
	}

	if int64(len(data)) > maximum {
		return nil, sizeFailure()
	}

	if response.StatusCode != http.StatusOK {
		return nil, requestFailure(ctx)
	}

	return data, nil
}
