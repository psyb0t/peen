//go:build browser

package browser_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

type browserClient struct {
	baseURL string
	client  *http.Client
}

type browserResponse struct {
	Data    json.RawMessage `json:"data"`
	Success bool            `json:"success"`
}

type browserLogResponse struct {
	Count int          `json:"count"`
	Log   []browserLog `json:"log"`
}

type browserLog struct {
	Method string `json:"method"`
	Status int    `json:"status"`
	Text   string `json:"text"`
	Type   string `json:"type"`
	URL    string `json:"url"`
}

type browserTextResponse struct {
	Text string `json:"text"`
}

type browserElementResponse struct {
	Text string `json:"text"`
}

func browserEvalStringResult(
	ctx context.Context,
	t *testing.T,
	expression string,
) string {
	t.Helper()

	data := browserAction(ctx, t, browserActionEval, map[string]any{
		browserExpressionKey: expression,
	})

	result := struct {
		Result string `json:"result"`
	}{}
	require.NoError(t, json.Unmarshal(data, &result))

	return result.Result
}

type browserAPIResponse struct {
	status int
	body   []byte
}

// browserAPIRequest posts one JSON body to the controller as the test token. A
// non-empty sessionID is sent as the session header.
func browserAPIRequest(
	ctx context.Context,
	t *testing.T,
	path string,
	body []byte,
	sessionID string,
) browserAPIResponse {
	t.Helper()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		integrationInfra.APIURL(path),
		bytes.NewReader(body),
	)
	require.NoError(t, err)
	request.Header.Set(aichteeteapee.HeaderNameContentType, aichteeteapee.ContentTypeJSON)
	request.Header.Set(
		aichteeteapee.HeaderNameAuthorization,
		browserBearerPrefix+testinfra.TestAPIToken,
	)
	if sessionID != "" {
		request.Header.Set(browserSessionIDHeader, sessionID)
	}

	response, err := integrationInfra.HTTPClient().Do(request)
	require.NoError(t, err)

	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)

	return browserAPIResponse{status: response.StatusCode, body: data}
}

func (c *browserClient) waitForReady(ctx context.Context) error {
	ticker := time.NewTicker(browserPollInterval)
	defer ticker.Stop()

	for {
		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			c.baseURL+browserHealthPath,
			nil,
		)
		if err != nil {
			return ctxerrors.Wrap(err, "create Stealthy browser readiness request")
		}

		response, requestErr := c.client.Do(request)
		if requestErr == nil {
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "wait for Stealthy browser readiness")
		case <-ticker.C:
		}
	}
}

func (c *browserClient) action(
	ctx context.Context,
	action string,
	parameters map[string]any,
) (json.RawMessage, error) {
	command := map[string]any{browserActionKey: action}
	for key, value := range parameters {
		command[key] = value
	}

	payload, err := json.Marshal(command)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal Stealthy browser command")
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+browserCommandPath,
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create Stealthy browser command request")
	}
	request.Header.Set(aichteeteapee.HeaderNameContentType, aichteeteapee.ContentTypeJSON)

	response, err := c.client.Do(request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "send Stealthy browser command")
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(
			wrapOptionalError(readErr, "read Stealthy browser command response"),
			wrapOptionalError(closeErr, "close Stealthy browser command response"),
		)
	}

	if response.StatusCode != http.StatusOK {
		return nil, ctxerrors.New("Stealthy browser command returned a non-OK status")
	}

	result := browserResponse{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, ctxerrors.Wrap(err, "decode Stealthy browser command response")
	}
	if !result.Success {
		return nil, ctxerrors.New("Stealthy browser command failed")
	}

	return result.Data, nil
}

func (c *browserClient) screenshot(ctx context.Context) ([]byte, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+browserScreenshotPath,
		nil,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create browser screenshot request")
	}

	response, err := c.client.Do(request)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "get browser screenshot")
	}

	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(
			wrapOptionalError(readErr, "read browser screenshot"),
			wrapOptionalError(closeErr, "close browser screenshot response"),
		)
	}
	if response.StatusCode != http.StatusOK {
		return nil, ctxerrors.New("browser screenshot returned a non-OK status")
	}

	return body, nil
}

func browserAction(
	ctx context.Context,
	t *testing.T,
	action string,
	parameters map[string]any,
) json.RawMessage {
	t.Helper()

	data, err := stealthyBrowser.action(ctx, action, parameters)
	require.NoErrorf(t, err, "browser action %q failed", action)

	return data
}

func browserText(ctx context.Context, t *testing.T) string {
	t.Helper()

	data := browserAction(ctx, t, browserActionGetText, nil)
	result := browserTextResponse{}
	require.NoError(t, json.Unmarshal(data, &result))

	return result.Text
}

func browserElement(ctx context.Context, t *testing.T, selector string) string {
	t.Helper()

	data := browserAction(ctx, t, browserActionGetElement, map[string]any{
		browserSelectorKey: selector,
	})
	result := browserElementResponse{}
	require.NoError(t, json.Unmarshal(data, &result))

	return result.Text
}

func browserNetworkLog(ctx context.Context, t *testing.T) []browserLog {
	t.Helper()

	data := browserAction(ctx, t, browserActionNetworkRead, nil)
	result := browserLogResponse{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, result.Count, len(result.Log))

	return result.Log
}

func browserConsoleLog(ctx context.Context, t *testing.T) []browserLog {
	t.Helper()

	data := browserAction(ctx, t, browserActionConsoleRead, nil)
	result := browserLogResponse{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, result.Count, len(result.Log))

	return result.Log
}
