package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	webFetchTestPage = `<!doctype html>
<html><head><title>  Example   Docs </title>
<style>body { color: red; }</style>
<script>alert("hidden")</script></head>
<body>
<nav><a href="#top">Skip</a></nav>
<h1>Install</h1>
<p>Run the   installer, then read the
<a href="/guide">guide</a> or <a href="javascript:void(0)">nothing</a>.</p>
<ul><li>First</li><li>Second</li></ul>
<pre>func main() {
    run()
}</pre>
<noscript>enable scripts</noscript>
</body></html>`
	webFetchTestText = "plain text body"
	webFetchTestJSON = `{"name":"example"}`
)

func newWebFetchServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeHTMLUTF8)
		writeWebFetchBody(t, w, webFetchTestPage)
	})
	mux.HandleFunc("/text", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeTextPlain)
		writeWebFetchBody(t, w, webFetchTestText)
	})
	mux.HandleFunc("/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeJSON)
		writeWebFetchBody(t, w, webFetchTestJSON)
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeImagePNG)
		writeWebFetchBody(t, w, "\x89PNG")
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeTextPlain)
		w.WriteHeader(http.StatusNotFound)
		writeWebFetchBody(t, w, "not here")
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/text", http.StatusFound)
	})
	mux.HandleFunc("/to-ftp", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://example.com/file", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/large", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(aichteeteapee.HeaderNameContentType,
			aichteeteapee.ContentTypeTextPlain)
		writeWebFetchBody(t, w, strings.Repeat("é", 8))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func writeWebFetchBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()

	_, err := w.Write([]byte(body))
	assert.NoError(t, err)
}

func TestWebFetchReducesAnHTMLPageToText(t *testing.T) {
	t.Parallel()
	server := newWebFetchServer(t)
	executor := newTestExecutor(t)

	output, err := executor.WebFetch(t.Context(),
		WebFetchInput{URL: server.URL + "/page"})
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, output.Status)
	assert.Equal(t, aichteeteapee.ContentTypeHTML, output.ContentType)
	assert.Equal(t, "Example Docs", output.Title)
	assert.False(t, output.Truncated)
	assert.Equal(t, `Skip

Install

Run the installer, then read the guide (`+server.URL+`/guide) or nothing.

- First

- Second

func main() {
    run()
}`, output.Text)
	assert.NotContains(t, output.Text, "alert")
	assert.NotContains(t, output.Text, "color: red")
	assert.NotContains(t, output.Text, "enable scripts")
}

func TestWebFetchReturnsTextFormatsAsTheyAre(t *testing.T) {
	t.Parallel()
	server := newWebFetchServer(t)
	executor := newTestExecutor(t)

	testCases := []struct {
		name            string
		path            string
		wantStatus      int
		wantContentType string
		wantText        string
		wantURLPath     string
	}{
		{
			name: "plain text", path: "/text", wantStatus: http.StatusOK,
			wantContentType: aichteeteapee.ContentTypeTextPlain,
			wantText:        webFetchTestText, wantURLPath: "/text",
		},
		{
			name: "json", path: "/json", wantStatus: http.StatusOK,
			wantContentType: aichteeteapee.ContentTypeJSON,
			wantText:        webFetchTestJSON, wantURLPath: "/json",
		},
		{
			name: "error status keeps its body", path: "/missing",
			wantStatus:      http.StatusNotFound,
			wantContentType: aichteeteapee.ContentTypeTextPlain,
			wantText:        "not here", wantURLPath: "/missing",
		},
		{
			name: "redirect reports the final URL", path: "/moved",
			wantStatus:      http.StatusOK,
			wantContentType: aichteeteapee.ContentTypeTextPlain,
			wantText:        webFetchTestText, wantURLPath: "/text",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := executor.WebFetch(t.Context(),
				WebFetchInput{URL: server.URL + tc.path})
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, output.Status)
			assert.Equal(t, tc.wantContentType, output.ContentType)
			assert.Equal(t, tc.wantText, output.Text)
			assert.Equal(t, server.URL+tc.wantURLPath, output.URL)
			assert.Empty(t, output.Title)
		})
	}
}

func TestWebFetchCutsTheBodyAtTheLimitOnACharacterBoundary(t *testing.T) {
	t.Parallel()
	server := newWebFetchServer(t)
	// Each "é" is two bytes, so a five-byte limit cuts the third in half.
	executor := newTestExecutorWithLimits(t, Limits{MaxFetchBytes: 5})

	output, err := executor.WebFetch(t.Context(),
		WebFetchInput{URL: server.URL + "/large"})
	require.NoError(t, err)
	assert.True(t, output.Truncated)
	assert.Equal(t, "éé", output.Text)
}

func TestWebFetchRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	server := newWebFetchServer(t)
	executor := newTestExecutor(t)

	testCases := []struct {
		name    string
		url     string
		wantErr error
	}{
		{name: "empty", url: "", wantErr: ErrUnsupportedURL},
		{name: "no host", url: "http://", wantErr: ErrUnsupportedURL},
		{name: "file scheme", url: "file:///etc/hostname", wantErr: ErrUnsupportedURL},
		{name: "ftp scheme", url: "ftp://example.com/x", wantErr: ErrUnsupportedURL},
		{
			name: "redirect to another scheme", url: server.URL + "/to-ftp",
			wantErr: ErrUnsupportedURL,
		},
		{
			name: "redirect loop", url: server.URL + "/loop",
			wantErr: commerr.ErrFetchFailed,
		},
		{
			name: "binary content", url: server.URL + "/binary",
			wantErr: ErrBinaryContent,
		},
		{
			name: "nothing listening", url: "http://127.0.0.1:1/",
			wantErr: commerr.ErrFetchFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := executor.WebFetch(t.Context(), WebFetchInput{URL: tc.url})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestWebFetchStopsWithItsContext(t *testing.T) {
	t.Parallel()
	server := newWebFetchServer(t)
	executor := newTestExecutor(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := executor.WebFetch(ctx, WebFetchInput{URL: server.URL + "/text"})
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, commerr.ErrFetchFailed)
}

func TestValidateLimitsRefusesANegativeFetchBound(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, ValidateLimits(Limits{MaxFetchBytes: -1}),
		ErrInvalidLimits)
	require.NoError(t, ValidateLimits(Limits{}))
}
