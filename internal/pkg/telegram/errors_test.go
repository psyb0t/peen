package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingBody struct {
	readError  error
	closeError error
}

func (b failingBody) Read([]byte) (int, error) {
	if b.readError != nil {
		return 0, b.readError
	}

	return 0, io.EOF
}

func (b failingBody) Close() error {
	return b.closeError
}

func assertSafeError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var urlError *url.Error
	assert.False(t, errors.As(err, &urlError))
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		assert.NotContains(t, cause.Error(), testToken)
		assert.NotContains(t, cause.Error(), apiBaseURL)
		assert.NotContains(t, cause.Error(), "private response")
	}
}

func TestTransportAndBodyErrorsStayPrivate(t *testing.T) {
	t.Parallel()
	external := &url.Error{
		Op: "Post", URL: apiBaseURL + "/bot" + testToken,
		Err: ctxerrors.New("private response"),
	}
	testCases := []struct {
		name           string
		transportError error
		body           io.ReadCloser
	}{
		{name: "transport", transportError: external},
		{name: "read", body: failingBody{readError: external}},
		{name: "close", body: failingBody{closeError: external}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, err := newClient(testToken, apiBaseURL, &http.Client{
				Transport: roundTripFunc(func(
					*http.Request,
				) (*http.Response, error) {
					if tc.transportError != nil {
						return nil, tc.transportError
					}

					return &http.Response{
						StatusCode: http.StatusOK, Body: tc.body,
					}, nil
				}),
			})
			require.NoError(t, err)
			_, err = client.SendMessage(t.Context(), 12, "hello")
			assertSafeError(t, err)
			assert.ErrorIs(t, err, commerr.ErrUnavailable)
		})
	}
}

func TestResponseErrorsStayPrivate(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		status int
		body   string
	}{
		{"status", http.StatusBadGateway, testToken},
		{"decode", http.StatusOK, testToken},
		{
			"description", http.StatusOK,
			`{"ok":false,"description":"` + testToken + `"}`,
		},
		{
			"wrong chat", http.StatusOK,
			`{"ok":true,"result":{"message_id":1,"chat":{"id":99}}}`,
		},
		{
			"zero message", http.StatusOK,
			`{"ok":true,"result":{"message_id":0,"chat":{"id":12}}}`,
		},
		{
			"result shape", http.StatusOK,
			`{"ok":true,"result":"` + testToken + `"}`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := testClient(t, func(
				w http.ResponseWriter, _ *http.Request,
			) {
				w.WriteHeader(tc.status)
				_, err := io.WriteString(w, tc.body)
				require.NoError(t, err)
			})
			_, err := client.SendMessage(t.Context(), 12, "hello")
			assertSafeError(t, err)
		})
	}
}

func TestRequestCancellationIsSafe(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	client := testClient(t, func(_ http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		require.NoError(t, err)
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		_, err := client.GetUpdates(ctx, 0, 50)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not start")
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
		assertSafeError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not stop")
	}
}

func TestJSONResponseSizeBound(t *testing.T) {
	t.Parallel()
	client := testClient(t, func(
		w http.ResponseWriter, _ *http.Request,
	) {
		_, err := io.WriteString(w, strings.Repeat("x", maxJSONBytes+1))
		require.NoError(t, err)
	})
	_, err := client.GetUpdates(t.Context(), 0, 0)
	require.ErrorIs(t, err, commerr.ErrOutOfRange)
	assertSafeError(t, err)
}

func TestDownloadReadErrorsStayPrivate(t *testing.T) {
	t.Parallel()
	client, err := newClient(testToken, apiBaseURL, &http.Client{
		Transport: roundTripFunc(func(
			r *http.Request,
		) (*http.Response, error) {
			var body io.ReadCloser
			if strings.HasSuffix(r.URL.Path, "/getFile") {
				body = io.NopCloser(strings.NewReader(
					`{"ok":true,"result":{` +
						`"file_id":"document","file_path":"a/b"}}`))
			} else {
				body = failingBody{readError: &url.Error{
					Op: "Get", URL: r.URL.String(), Err: commerr.ErrUnavailable,
				}}
			}

			return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
		}),
	})
	require.NoError(t, err)
	_, err = client.Download(t.Context(), "document", 10)
	require.ErrorIs(t, err, commerr.ErrUnavailable)
	assertSafeError(t, err)
}

func TestPollingDeadlinesAreBounded(t *testing.T) {
	t.Parallel()
	for _, seconds := range []int{0, 30, 50} {
		t.Run(strconv.Itoa(seconds), func(t *testing.T) {
			t.Parallel()
			client, err := newClient(testToken, apiBaseURL, &http.Client{
				Transport: roundTripFunc(func(
					r *http.Request,
				) (*http.Response, error) {
					deadline, exists := r.Context().Deadline()
					require.True(t, exists)
					maximum := regularTimeout
					if seconds > 0 {
						maximum = time.Duration(seconds) * time.Second
						maximum += pollSlack
					}
					assert.LessOrEqual(t, time.Until(deadline), maximum)
					assert.Greater(t, time.Until(deadline),
						maximum-5*time.Second)

					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(strings.NewReader(
							`{"ok":true,"result":[]}`)),
					}, nil
				}),
			})
			require.NoError(t, err)
			updates, err := client.GetUpdates(t.Context(), 0, seconds)
			require.NoError(t, err)
			assert.Empty(t, updates)
		})
	}
}
