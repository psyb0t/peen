package dockerproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAPIVersion = "/v1.43"

	// testSecretValue stands in for a credential in a container environment.
	// It is fixed test data, and the tests assert it never leaves the proxy.
	testSecretValue = "hunter2-not-a-real-secret" //nolint:gosec // Test fixture.

	holdQuery        = "hold"
	brokenContainer  = "broken"
	firstEventChunk  = `{"status":"start","id":"abc"}` + "\n"
	secondEventChunk = `{"status":"die","id":"abc"}` + "\n"
	streamTimeout    = 5 * time.Second
	fakeDaemonSocket = "daemon.sock"
	proxySocketName  = "docker.sock"
)

// inspectFixture is what the fake daemon answers for every ordinary request.
// List endpoints pass it through untouched, inspect endpoints redact it.
var inspectFixture = `{"Id":"abc","Name":"/web",` + //nolint:gochecknoglobals // Shared fixture.
	`"Config":{"Env":["SECRET_TOKEN=` + testSecretValue + `","PATH=/usr/bin",` +
	`"EMPTY=","BARE","EQUALS=a=b"],"Image":"web:1","Labels":{"note":"<b>&"}},` +
	`"ContainerConfig":{"Env":["OTHER=` + testSecretValue + `"],"Cmd":null},` +
	`"State":{"Running":true,"Pid":9007199254740993},"HostConfig":{"Memory":0}}`

type receivedRequest struct {
	method string
	path   string
	header http.Header
}

// fakeDaemon is an Engine API stand-in on a Unix socket that records every
// request that reaches it. It proves what the proxy forwards, not how Docker
// behaves.
type fakeDaemon struct {
	socketPath string
	release    chan struct{}

	mutex    sync.Mutex
	received []receivedRequest
}

func startFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()

	daemon := &fakeDaemon{
		socketPath: filepath.Join(shortTempDir(t), fakeDaemonSocket),
		release:    make(chan struct{}),
	}

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(t.Context(), "unix", daemon.socketPath)
	require.NoError(t, err)

	server := &http.Server{
		Handler:           daemon,
		ReadHeaderTimeout: streamTimeout,
	}

	served := make(chan error, 1)

	go func() {
		served <- server.Serve(listener)
	}()

	t.Cleanup(func() {
		assert.NoError(t, server.Close())
		assert.ErrorIs(t, <-served, http.ErrServerClosed)
	})

	return daemon
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mutex.Lock()
	d.received = append(d.received, receivedRequest{
		method: r.Method,
		path:   r.URL.Path,
		header: r.Header.Clone(),
	})
	d.mutex.Unlock()

	if strings.HasSuffix(r.URL.Path, "/"+segmentEvents) {
		d.serveEvents(w, r)

		return
	}

	if strings.Contains(r.URL.Path, "/"+brokenContainer+"/") {
		fakeWrite(w, "SECRET_TOKEN="+testSecretValue)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	fakeWrite(w, inspectFixture)
}

// fakeWrite answers for the fake daemon. A failed write means the proxy hung
// up, and every test observes that from the client side instead.
func fakeWrite(w io.Writer, text string) {
	if _, err := io.WriteString(w, text); err != nil {
		return
	}
}

// serveEvents sends one event at once. With the hold query it then keeps the
// stream open until the test releases it or the connection ends, the way a
// real daemon keeps /events open.
func (d *fakeDaemon) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	fakeWrite(w, firstEventChunk)
	flusher.Flush()

	if r.URL.RawQuery != holdQuery {
		return
	}

	select {
	case <-d.release:
		fakeWrite(w, secondEventChunk)
	case <-r.Context().Done():
	}
}

func (d *fakeDaemon) requests() []receivedRequest {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	return append([]receivedRequest(nil), d.received...)
}

// shortTempDir keeps socket paths under the 107-byte Unix address limit,
// which t.TempDir cannot promise for long test names.
func shortTempDir(t *testing.T) string {
	t.Helper()

	directory, err := os.MkdirTemp("", "peen-dp-")
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(directory)) })

	return directory
}

func startProxy(t *testing.T, daemon *fakeDaemon) *Server {
	t.Helper()

	server, err := Start(
		t.Context(),
		filepath.Join(shortTempDir(t), proxySocketName),
		daemon.socketPath,
	)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, server.Close()) })

	return server
}

// rawResponse is the part of a response the tests read, taken after the body
// is fully read and closed.
type rawResponse struct {
	StatusCode    int
	Header        http.Header
	ContentLength int64
}

// rawRequest writes one request byte for byte, so the target reaches the
// proxy exactly as written, with no client-side cleaning or escaping.
func rawRequest(
	t *testing.T,
	socketPath, method, target string,
	headers map[string]string,
) (rawResponse, []byte) {
	t.Helper()

	dialer := net.Dialer{}

	conn, err := dialer.DialContext(t.Context(), "unix", socketPath)
	require.NoError(t, err)

	defer func() { assert.NoError(t, conn.Close()) }()

	header := http.Header{"Host": {"docker"}, "Connection": {"close"}}
	for name, value := range headers {
		header.Set(name, value)
	}

	_, err = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\n", method, target)
	require.NoError(t, err)
	require.NoError(t, header.Write(conn))

	_, err = io.WriteString(conn, "\r\n")
	require.NoError(t, err)

	response, err := http.ReadResponse(
		bufio.NewReader(conn),
		&http.Request{Method: method},
	)
	require.NoError(t, err)

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	return rawResponse{
		StatusCode:    response.StatusCode,
		Header:        response.Header,
		ContentLength: response.ContentLength,
	}, body
}

func proxyClient(socketPath string) *http.Client {
	dialer := net.Dialer{}

	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(
				ctx context.Context,
				_, _ string,
			) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func TestProxyForwardsAllowedReads(t *testing.T) {
	t.Parallel()

	paths := []string{
		"/_ping",
		"/version",
		"/info",
		"/events",
		"/system/df",
		"/containers/json",
		"/containers/abc123/json",
		"/containers/abc123/stats",
		"/images/json",
		"/images/ubuntu/json",
		"/images/registry.example.com:5000/team/app:1.2/json",
		"/images/sha256:0123abcd/json",
		"/networks",
		"/networks/bridge",
		"/volumes",
		"/volumes/data",
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, prefix := range []string{"", testAPIVersion} {
			for _, requestPath := range paths {
				target := prefix + requestPath

				t.Run(method+" "+target, func(t *testing.T) {
					t.Parallel()

					daemon := startFakeDaemon(t)
					proxy := startProxy(t, daemon)

					response, _ := rawRequest(
						t,
						proxy.SocketPath(),
						method,
						target,
						nil,
					)
					assert.Equal(t, http.StatusOK, response.StatusCode)

					received := daemon.requests()
					require.Len(t, received, 1)
					assert.Equal(t, method, received[0].method)
					assert.Equal(t, target, received[0].path)
				})
			}
		}
	}
}

func TestProxyRefusesWithoutReachingTheDaemon(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
		reason  string
	}{
		{name: "create", method: http.MethodPost, target: "/containers/create", reason: reasonMethod},
		{name: "start", method: http.MethodPost, target: "/v1.43/containers/abc/start", reason: reasonMethod},
		{name: "kill", method: http.MethodPost, target: "/containers/abc/kill", reason: reasonMethod},
		{name: "remove", method: http.MethodDelete, target: "/containers/abc", reason: reasonMethod},
		{name: "exec create", method: http.MethodPost, target: "/containers/abc/exec", reason: reasonMethod},
		{name: "archive put", method: http.MethodPut, target: "/containers/abc/archive", reason: reasonMethod},
		{name: "image pull", method: http.MethodPost, target: "/images/create", reason: reasonMethod},
		{name: "build", method: http.MethodPost, target: "/build", reason: reasonMethod},
		{name: "patch", method: http.MethodPatch, target: "/containers/json", reason: reasonMethod},
		{name: "options on an allowed path", method: http.MethodOptions, target: "/_ping", reason: reasonMethod},
		{name: "post to an allowed path", method: http.MethodPost, target: "/containers/json", reason: reasonMethod},
		{name: "logs", method: http.MethodGet, target: "/containers/abc/logs", reason: reasonEndpoint},
		{name: "top", method: http.MethodGet, target: "/containers/abc/top", reason: reasonEndpoint},
		{name: "changes", method: http.MethodGet, target: "/containers/abc/changes", reason: reasonEndpoint},
		{name: "archive", method: http.MethodGet, target: "/containers/abc/archive", reason: reasonEndpoint},
		{name: "export", method: http.MethodGet, target: "/v1.43/containers/abc/export", reason: reasonEndpoint},
		{name: "attach websocket", method: http.MethodGet, target: "/containers/abc/attach/ws", reason: reasonEndpoint},
		{name: "exec inspect", method: http.MethodGet, target: "/exec/abc/json", reason: reasonEndpoint},
		{name: "services", method: http.MethodGet, target: "/services", reason: reasonEndpoint},
		{name: "tasks", method: http.MethodGet, target: "/tasks", reason: reasonEndpoint},
		{name: "secrets", method: http.MethodGet, target: "/secrets", reason: reasonEndpoint},
		{name: "configs", method: http.MethodGet, target: "/configs", reason: reasonEndpoint},
		{name: "nodes", method: http.MethodGet, target: "/nodes", reason: reasonEndpoint},
		{name: "swarm", method: http.MethodGet, target: "/swarm", reason: reasonEndpoint},
		{name: "plugins", method: http.MethodGet, target: "/plugins", reason: reasonEndpoint},
		{name: "distribution", method: http.MethodGet, target: "/distribution/alpine/json", reason: reasonEndpoint},
		{name: "image save all", method: http.MethodGet, target: "/images/get", reason: reasonEndpoint},
		{name: "image save one", method: http.MethodGet, target: "/images/alpine/get", reason: reasonEndpoint},
		{name: "image history", method: http.MethodGet, target: "/images/alpine/history", reason: reasonEndpoint},
		{name: "image search", method: http.MethodGet, target: "/images/search", reason: reasonEndpoint},
		{name: "session", method: http.MethodGet, target: "/session", reason: reasonEndpoint},
		{name: "container collection", method: http.MethodGet, target: "/containers", reason: reasonEndpoint},
		{name: "container without endpoint", method: http.MethodGet, target: "/containers/abc", reason: reasonEndpoint},
		{name: "container id with slash", method: http.MethodGet, target: "/containers/a/b/json", reason: reasonEndpoint},
		{name: "image without endpoint", method: http.MethodGet, target: "/images/alpine", reason: reasonEndpoint},
		{name: "stats suffix", method: http.MethodGet, target: "/containers/abc/stats/more", reason: reasonEndpoint},
		{name: "network subpath", method: http.MethodGet, target: "/networks/bridge/extra", reason: reasonEndpoint},
		{name: "system other", method: http.MethodGet, target: "/system/other", reason: reasonEndpoint},
		{name: "root", method: http.MethodGet, target: "/", reason: reasonEndpoint},
		{name: "version only", method: http.MethodGet, target: "/v1.43", reason: reasonEndpoint},
		{name: "major only version", method: http.MethodGet, target: "/v1/_ping", reason: reasonEndpoint},
		{name: "three part version", method: http.MethodGet, target: "/v1.43.1/_ping", reason: reasonEndpoint},
		{name: "letter version", method: http.MethodGet, target: "/vx.y/_ping", reason: reasonEndpoint},
		{name: "dot dot", method: http.MethodGet, target: "/containers/../images/json", reason: reasonUncleanPath},
		{name: "dot dot after version", method: http.MethodGet, target: "/v1.43/containers/abc/../../build", reason: reasonUncleanPath},
		{name: "double slash", method: http.MethodGet, target: "//containers/json", reason: reasonUncleanPath},
		{name: "trailing slash", method: http.MethodGet, target: "/containers/json/", reason: reasonUncleanPath},
		{name: "dot segment", method: http.MethodGet, target: "/./_ping", reason: reasonUncleanPath},
		{name: "encoded slash", method: http.MethodGet, target: "/containers/a%2Fb/json", reason: reasonEncodedPath},
		{name: "encoded dot dot", method: http.MethodGet, target: "/containers/%2e%2e/build", reason: reasonEncodedPath},
		{
			name:    "upgrade header",
			method:  http.MethodGet,
			target:  "/containers/abc/json",
			headers: map[string]string{"Upgrade": "tcp"},
			reason:  reasonUpgrade,
		},
		{
			name:    "connection upgrade",
			method:  http.MethodGet,
			target:  "/_ping",
			headers: map[string]string{"Connection": "keep-alive, Upgrade"},
			reason:  reasonUpgrade,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			daemon := startFakeDaemon(t)
			proxy := startProxy(t, daemon)

			response, body := rawRequest(
				t,
				proxy.SocketPath(),
				tc.method,
				tc.target,
				tc.headers,
			)

			assert.Equal(t, http.StatusForbidden, response.StatusCode)
			assert.Equal(
				t,
				"application/json",
				response.Header.Get("Content-Type"),
			)

			refusal := errorBody{}
			require.NoError(t, json.Unmarshal(body, &refusal))
			assert.Equal(t, RefusalPrefix+tc.reason, refusal.Message)

			assert.Empty(t, daemon.requests(), "nothing may reach the daemon")
		})
	}
}

// fixtureWithoutEnv decodes a document and drops both environment lists, so
// what is left must match field for field after redaction.
func fixtureWithoutEnv(t *testing.T, body []byte) map[string]any {
	t.Helper()

	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()

	document := map[string]any{}
	require.NoError(t, decoder.Decode(&document))

	for _, key := range []string{inspectConfigKey, inspectContainerConfigKey} {
		config, ok := document[key].(map[string]any)
		require.True(t, ok, "%s must stay an object", key)

		delete(config, inspectEnvKey)
	}

	return document
}

func envOf(t *testing.T, body []byte, key string) []string {
	t.Helper()

	document := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(body, &document))

	config := struct {
		Env []string `json:"Env"` //nolint:tagliatelle // Docker field name.
	}{}
	require.NoError(t, json.Unmarshal(document[key], &config))

	return config.Env
}

func TestProxyRedactsInspectEnvironment(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/containers/abc/json",
		"/v1.43/containers/abc/json",
		"/images/registry.example.com:5000/team/app:1.2/json",
		"/v1.43/images/ubuntu/json",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			daemon := startFakeDaemon(t)
			proxy := startProxy(t, daemon)

			response, body := rawRequest(
				t,
				proxy.SocketPath(),
				http.MethodGet,
				target,
				map[string]string{"Accept-Encoding": "gzip"},
			)
			require.Equal(t, http.StatusOK, response.StatusCode)

			assert.NotContains(t, string(body), testSecretValue)
			assert.Equal(
				t,
				[]string{
					"SECRET_TOKEN=" + RedactedValue,
					"PATH=" + RedactedValue,
					"EMPTY=" + RedactedValue,
					"BARE",
					"EQUALS=" + RedactedValue,
				},
				envOf(t, body, inspectConfigKey),
			)
			assert.Equal(
				t,
				[]string{"OTHER=" + RedactedValue},
				envOf(t, body, inspectContainerConfigKey),
			)

			assert.Equal(
				t,
				fixtureWithoutEnv(t, []byte(inspectFixture)),
				fixtureWithoutEnv(t, body),
				"every field but the environment must survive",
			)

			assert.Equal(
				t,
				strconv.Itoa(len(body)),
				response.Header.Get("Content-Length"),
			)
			assert.EqualValues(t, len(body), response.ContentLength)

			received := daemon.requests()
			require.Len(t, received, 1)
			assert.Empty(
				t,
				received[0].header.Get("Accept-Encoding"),
				"the daemon must answer uncompressed so the body can be read",
			)
		})
	}
}

// A list endpoint is passed through as the daemon sent it. Only inspect
// documents carry the environment, so only they are rewritten.
func TestProxyPassesListResponsesThrough(t *testing.T) {
	t.Parallel()

	daemon := startFakeDaemon(t)
	proxy := startProxy(t, daemon)

	response, body := rawRequest(
		t,
		proxy.SocketPath(),
		http.MethodGet,
		"/containers/json",
		nil,
	)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, inspectFixture, string(body))
}

// An inspect body the proxy cannot decode is refused rather than passed
// through, because passing it through would pass its values through too.
func TestProxyFailsClosedOnAnUndecodableInspect(t *testing.T) {
	t.Parallel()

	daemon := startFakeDaemon(t)
	proxy := startProxy(t, daemon)

	response, body := rawRequest(
		t,
		proxy.SocketPath(),
		http.MethodGet,
		"/containers/"+brokenContainer+"/json",
		nil,
	)

	assert.Equal(t, http.StatusBadGateway, response.StatusCode)
	assert.NotContains(t, string(body), testSecretValue)

	failure := errorBody{}
	require.NoError(t, json.Unmarshal(body, &failure))
	assert.Equal(t, upstreamFailureMessage, failure.Message)
}

func TestProxyAnswersBadGatewayWhenTheDaemonIsGone(t *testing.T) {
	t.Parallel()

	server, err := Start(
		t.Context(),
		filepath.Join(shortTempDir(t), proxySocketName),
		filepath.Join(shortTempDir(t), "missing.sock"),
	)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, server.Close()) })

	response, body := rawRequest(
		t,
		server.SocketPath(),
		http.MethodGet,
		"/_ping",
		nil,
	)

	assert.Equal(t, http.StatusBadGateway, response.StatusCode)

	failure := errorBody{}
	require.NoError(t, json.Unmarshal(body, &failure))
	assert.Equal(t, upstreamFailureMessage, failure.Message)
}

// openEventStream starts a held /events request and returns a reader that has
// already received the first event. The body closes when the test ends.
func openEventStream(t *testing.T, proxy *Server) *bufio.Reader {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://docker/v1.43/events?"+holdQuery,
		http.NoBody,
	)
	require.NoError(t, err)

	response, err := proxyClient(proxy.SocketPath()).Do(request)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, response.Body.Close()) })

	require.Equal(t, http.StatusOK, response.StatusCode)

	reader := bufio.NewReader(response.Body)

	first := readLine(t, reader)
	require.NoError(t, first.err)
	require.Equal(t, firstEventChunk, first.line)

	return reader
}

type lineResult struct {
	line string
	err  error
}

// readLine reads one line without letting a stuck stream hang the test.
func readLine(t *testing.T, reader *bufio.Reader) lineResult {
	t.Helper()

	result := make(chan lineResult, 1)

	go func() {
		line, err := reader.ReadString('\n')
		result <- lineResult{line: line, err: err}
	}()

	select {
	case got := <-result:
		return got
	case <-time.After(streamTimeout):
		t.Fatal("the stream delivered nothing in time")

		return lineResult{}
	}
}

// The first event reaches the client while the daemon still holds the stream
// open, which is what makes `docker events` usable through the proxy.
func TestProxyStreamsEventsBeforeTheDaemonFinishes(t *testing.T) {
	t.Parallel()

	daemon := startFakeDaemon(t)
	proxy := startProxy(t, daemon)

	reader := openEventStream(t, proxy)

	close(daemon.release)

	second := readLine(t, reader)
	require.NoError(t, second.err)
	assert.Equal(t, secondEventChunk, second.line)

	rest := readLine(t, reader)
	assert.ErrorIs(t, rest.err, io.EOF)
}

func TestProxyCloseEndsAStreamAndRemovesTheSocket(t *testing.T) {
	t.Parallel()

	daemon := startFakeDaemon(t)

	proxy, err := Start(
		t.Context(),
		filepath.Join(shortTempDir(t), proxySocketName),
		daemon.socketPath,
	)
	require.NoError(t, err)

	reader := openEventStream(t, proxy)

	require.NoError(t, proxy.Close())

	ended := readLine(t, reader)
	require.Error(t, ended.err, "a closed proxy must end the stream")
	assert.Empty(t, ended.line)

	_, err = os.Stat(proxy.SocketPath())
	require.ErrorIs(t, err, fs.ErrNotExist)

	require.NoError(t, proxy.Close(), "a second Close is harmless")

	dialer := net.Dialer{}
	_, err = dialer.DialContext(t.Context(), "unix", proxy.SocketPath())
	require.Error(t, err, "nothing may listen after Close")
}

func TestProxySocketIsPrivate(t *testing.T) {
	t.Parallel()

	proxy := startProxy(t, startFakeDaemon(t))

	info, err := os.Stat(proxy.SocketPath())
	require.NoError(t, err)

	assert.NotZero(t, info.Mode()&os.ModeSocket)
	assert.Equal(t, socketMode, info.Mode().Perm())
}

// A socket file left by a controller that died does not stop the next
// generation of the same session from starting its proxy.
func TestStartReplacesAStaleSocketFile(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(shortTempDir(t), proxySocketName)
	require.NoError(t, os.WriteFile(socketPath, nil, socketMode))

	server, err := Start(t.Context(), socketPath, startFakeDaemon(t).socketPath)
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, server.Close()) })

	response, _ := rawRequest(t, socketPath, http.MethodGet, "/_ping", nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
}

func TestStartRefusesRelativePaths(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		socket   string
		upstream string
	}{
		{name: "relative socket", socket: "docker.sock", upstream: "/var/run/docker.sock"},
		{name: "relative upstream", socket: "/run/peen/docker.sock", upstream: "docker.sock"},
		{name: "empty upstream", socket: "/run/peen/docker.sock"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Start(t.Context(), tc.socket, tc.upstream)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}
