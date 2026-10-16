package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client still built against the previous /v1 split must fail loudly
// instead of silently receiving the app shell. Every stale prefix answers a
// JSON 404 with the aichteeteapee not-found code, for both the bare prefix
// and a path underneath it, and regardless of method.
func TestSPAHandlerRejectsStaleAPIPrefixesAsJSON(t *testing.T) {
	t.Parallel()

	handler, err := newSPAHandler()
	require.NoError(t, err)

	testCases := []struct {
		name string
		path string
	}{
		{name: "bare v1", path: "/v1"},
		{name: "v1 with subpath", path: "/v1/messages"},
		{name: "bare api", path: "/api"},
		{name: "api with subpath", path: "/api/v2/messages"},
		{name: "bare ws", path: "/ws"},
		{name: "ws with subpath", path: "/ws/v1"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, method := range []string{
				http.MethodGet,
				http.MethodPost,
			} {
				t.Run(method, func(t *testing.T) {
					t.Parallel()

					request := httptest.NewRequestWithContext(
						t.Context(),
						method,
						tc.path,
						nil,
					)
					recorder := httptest.NewRecorder()

					handler.ServeHTTP(recorder, request)

					assert.Equal(t, http.StatusNotFound, recorder.Code)
					assertErrorCode(
						t,
						recorder,
						aichteeteapee.ErrorCodeNotFound,
					)
				})
			}
		})
	}
}

// The root and a client-side deep link still resolve to the app shell: the
// SPA catch-all only refuses the stale API-shaped prefixes above.
func TestSPAHandlerStillServesDeepLinks(t *testing.T) {
	t.Parallel()

	handler, err := newSPAHandler()
	require.NoError(t, err)

	indexRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/",
		nil,
	)
	indexRecorder := httptest.NewRecorder()
	handler.ServeHTTP(indexRecorder, indexRequest)

	require.Equal(t, http.StatusOK, indexRecorder.Code)
	indexBody := indexRecorder.Body.String()
	require.NotEmpty(t, indexBody)

	testCases := []struct {
		name string
		path string
	}{
		{name: "root with a session query", path: "/?session=" + uuid.New().String()},
		{name: "nested client route", path: "/some/client/route"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				tc.path,
				nil,
			)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, indexBody, recorder.Body.String())
		})
	}
}

func TestIsStaleAPIPath(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		path string
		want bool
	}{
		{name: "bare v1", path: "/v1", want: true},
		{name: "v1 prefix", path: "/v1/messages", want: true},
		{name: "bare api", path: "/api", want: true},
		{name: "api prefix", path: "/api/v1/messages", want: true},
		{name: "bare ws", path: "/ws", want: true},
		{name: "ws prefix", path: "/ws/v1", want: true},
		{name: "root", path: "/"},
		{name: "deep link", path: "/some/client/route"},
		{name: "a prefix match is not enough", path: "/v1x"},
		{name: "empty", path: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, isStaleAPIPath(tc.path))
		})
	}
}
