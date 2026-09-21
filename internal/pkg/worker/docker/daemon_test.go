package docker_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/worker/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engineAPIPrefix is the Engine API version the client pins. It is spelled out
// here rather than imported so an accidental version bump fails these tests
// instead of silently changing which daemon API Peen talks to.
const engineAPIPrefix = "/v1.43"

// headerBytes is the daemon log frame header size.
const headerBytes = 8

// fakeEngine is an Engine API stand-in on a real unix socket, so the client's
// own transport, request building, and status handling all run. A stubbed
// interface would skip exactly the layer these tests exist to check.
type fakeEngine struct {
	requests chan *http.Request
	handler  http.HandlerFunc
}

func newFakeEngine(t *testing.T, handler http.HandlerFunc) (*fakeEngine, string) {
	t.Helper()

	// The socket lives in its own short temp dir because a unix socket path
	// has a low length limit that a nested test name would blow past.
	socketPath := filepath.Join(t.TempDir(), "d.sock")

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(
		context.Background(),
		"unix",
		socketPath,
	)
	require.NoError(t, err)

	engine := &fakeEngine{
		requests: make(chan *http.Request, 16),
		handler:  handler,
	}

	server := &httptest.Server{
		Listener: listener,
		Config: &http.Server{
			Handler: http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					engine.requests <- r
					engine.handler(w, r)
				},
			),
			ReadHeaderTimeout: 0,
		},
	}
	server.Start()
	t.Cleanup(server.Close)

	return engine, socketPath
}

func (f *fakeEngine) lastRequest(t *testing.T) *http.Request {
	t.Helper()

	select {
	case request := <-f.requests:
		return request
	default:
		t.Fatal("the client sent no request to the daemon")

		return nil
	}
}

// A client without a socket path is refused at construction, because a
// controller with no Docker authority must never produce a usable launcher.
func TestNewDaemonClientRefusesAnEmptySocketPath(t *testing.T) {
	t.Parallel()

	client, err := docker.NewDaemonClient("")
	require.Error(t, err)
	assert.Nil(t, client)
}

// Each lifecycle call has to reach its own Engine API endpoint with its own
// method. A wrong verb or path would stop or remove the wrong thing.
func TestDaemonClientCallsTheRightEndpointForEachLifecycleStep(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	testCases := []struct {
		name       string
		invoke     func(docker.Client) error
		wantMethod string
		wantPath   string
	}{
		{
			name: "start",
			invoke: func(c docker.Client) error {
				return c.StartContainer(ctx, "abc123")
			},
			wantMethod: http.MethodPost,
			wantPath:   engineAPIPrefix + "/containers/abc123/start",
		},
		{
			name: "stop",
			invoke: func(c docker.Client) error {
				return c.StopContainer(ctx, "abc123")
			},
			wantMethod: http.MethodPost,
			wantPath:   engineAPIPrefix + "/containers/abc123/stop",
		},
		{
			name: "remove",
			invoke: func(c docker.Client) error {
				return c.RemoveContainer(ctx, "abc123")
			},
			wantMethod: http.MethodDelete,
			wantPath:   engineAPIPrefix + "/containers/abc123",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			engine, socketPath := newFakeEngine(
				t,
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				},
			)

			client, err := docker.NewDaemonClient(socketPath)
			require.NoError(t, err)
			require.NoError(t, tc.invoke(client))

			request := engine.lastRequest(t)
			assert.Equal(t, tc.wantMethod, request.Method)
			assert.Equal(t, tc.wantPath, request.URL.Path)
		})
	}
}

// Inspect maps the daemon's nested JSON onto the flat state the launcher uses.
// The labels matter most: Peen only stops or removes a container while its own
// labels still match, so losing them would disarm that check.
func TestDaemonClientInspectsAContainerIncludingItsLabels(t *testing.T) {
	t.Parallel()

	_, socketPath := newFakeEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":    "abc123",
			"Image": "sha256:animage",
			"State": map[string]any{"Running": true, "ExitCode": 0},
			"Config": map[string]any{
				"Labels": map[string]string{
					"peen.managed": "true",
					"peen.session": "a-session",
				},
			},
		})
	})

	client, err := docker.NewDaemonClient(socketPath)
	require.NoError(t, err)

	state, err := client.InspectContainer(context.Background(), "abc123")
	require.NoError(t, err)
	assert.Equal(t, "abc123", state.ID)
	assert.True(t, state.Running)
	assert.Equal(t, 0, state.ExitCode)
	assert.Equal(t, "sha256:animage", state.ImageID)
	assert.Equal(t, "true", state.Labels["peen.managed"])
	assert.Equal(t, "a-session", state.Labels["peen.session"])
}

// An image reference contains slashes and a colon, so it has to be escaped
// into the path. An unescaped reference would address the wrong endpoint.
func TestDaemonClientEscapesAnImageReferenceIntoThePath(t *testing.T) {
	t.Parallel()

	engine, socketPath := newFakeEngine(
		t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:theimage",
				"RepoDigests": []string{"psyb0t/peen@sha256:thedigest"},
			})
		},
	)

	client, err := docker.NewDaemonClient(socketPath)
	require.NoError(t, err)

	identity, err := client.InspectImage(
		context.Background(),
		"psyb0t/peen:v1.2.3",
	)
	require.NoError(t, err)
	assert.Equal(t, "sha256:theimage", identity.ID)
	require.Len(t, identity.RepoDigests, 1)

	request := engine.lastRequest(t)
	assert.Equal(
		t,
		engineAPIPrefix+"/images/psyb0t/peen:v1.2.3/json",
		request.URL.Path,
	)
}

// A daemon error status becomes an error rather than a zero value, so a
// controller never treats a refused create as a started worker.
func TestDaemonClientTurnsAnErrorStatusIntoAnError(t *testing.T) {
	t.Parallel()

	_, socketPath := newFakeEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"daemon is unwell"}`))
	})

	client, err := docker.NewDaemonClient(socketPath)
	require.NoError(t, err)

	err = client.StartContainer(context.Background(), "abc123")
	require.Error(t, err)

	state, err := client.InspectContainer(context.Background(), "abc123")
	require.Error(t, err)
	assert.Empty(t, state.ID)
}

// Create returns the daemon's container ID, which is what every later call
// addresses. Returning an empty ID would leave a running container nothing
// could stop.
func TestDaemonClientReturnsTheCreatedContainerID(t *testing.T) {
	t.Parallel()

	engine, socketPath := newFakeEngine(
		t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "created123"})
		},
	)

	client, err := docker.NewDaemonClient(socketPath)
	require.NoError(t, err)

	id, err := client.CreateContainer(context.Background(), docker.CreateRequest{
		Name:  "peen-worker-test",
		Image: "psyb0t/peen:v1.2.3",
	})
	require.NoError(t, err)
	assert.Equal(t, "created123", id)

	request := engine.lastRequest(t)
	assert.Equal(t, http.MethodPost, request.Method)
	assert.Equal(t, engineAPIPrefix+"/containers/create", request.URL.Path)
	assert.Equal(
		t,
		"peen-worker-test",
		request.URL.Query().Get("name"),
		"the container name is what makes it findable and label-checkable",
	)
}

// A pull by tag and a pull by digest both go to /images/create, but the
// reference has to be split into fromImage and tag. Sending the whole
// reference as fromImage makes the daemon pull the wrong thing or nothing.
func TestDaemonClientSplitsAPullReferenceIntoItsQuery(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		reference     string
		wantFromImage string
		wantTag       string
	}{
		{
			name:          "by tag",
			reference:     "psyb0t/peen:v1.2.3",
			wantFromImage: "psyb0t/peen",
			wantTag:       "v1.2.3",
		},
		{
			name:          "by digest",
			reference:     "psyb0t/peen@sha256:abc",
			wantFromImage: "psyb0t/peen",
			wantTag:       "sha256:abc",
		},
		{
			name:          "no tag at all",
			reference:     "psyb0t/peen",
			wantFromImage: "psyb0t/peen",
			wantTag:       "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			engine, socketPath := newFakeEngine(
				t,
				func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{"status":"Download complete"}`))
				},
			)

			client, err := docker.NewDaemonClient(socketPath)
			require.NoError(t, err)
			require.NoError(
				t,
				client.PullImage(context.Background(), tc.reference),
			)

			request := engine.lastRequest(t)
			assert.Equal(
				t,
				engineAPIPrefix+"/images/create",
				request.URL.Path,
			)
			assert.Equal(
				t,
				tc.wantFromImage,
				request.URL.Query().Get("fromImage"),
			)
			assert.Equal(t, tc.wantTag, request.URL.Query().Get("tag"))
		})
	}
}

// The pull endpoint answers 200 and then reports failure inside the progress
// stream. Trusting the status alone would let a failed pull look successful
// and the create that follows would fail with a confusing "no such image".
func TestDaemonClientFailsOnAnErrorInsideThePullProgress(t *testing.T) {
	t.Parallel()

	_, socketPath := newFakeEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`{"status":"Pulling"}` + "\n" +
				`{"error":"denied: requested access to the resource is denied"}`,
		))
	})

	client, err := docker.NewDaemonClient(socketPath)
	require.NoError(t, err)

	err = client.PullImage(context.Background(), "psyb0t/peen:v1.2.3")
	require.Error(t, err)

	// The daemon's own text can name a registry host and credential state, so
	// it must not be echoed into the error.
	assert.NotContains(t, err.Error(), "denied")
	assert.Contains(t, err.Error(), "psyb0t/peen:v1.2.3")
}

// The daemon multiplexes container logs into 8-byte-headered frames. Handing
// those bytes to a caller raw would splice binary headers into the log text.
func TestDaemonClientDemultiplexesTheLogStream(t *testing.T) {
	t.Parallel()

	stdout := []byte("hello from stdout\n")
	stderr := []byte("and stderr\n")

	// The daemon prefixes every chunk with an 8-byte header: the stream id in
	// byte 0, then the payload length as a big-endian uint32 in bytes 4..7.
	frame := func(stream byte, payload []byte) []byte {
		framed := make([]byte, headerBytes, headerBytes+len(payload))
		framed[0] = stream
		binary.BigEndian.PutUint32(
			framed[4:],
			//nolint:gosec // Fixture payloads are a few bytes long.
			uint32(len(payload)),
		)

		return append(framed, payload...)
	}

	_, socketPath := newFakeEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(frame(1, stdout))
		_, _ = w.Write(frame(2, stderr))
	})

	client, err := docker.NewDaemonClient(socketPath)
	require.NoError(t, err)

	stream, err := client.StreamLogs(context.Background(), "abc123")
	require.NoError(t, err)

	t.Cleanup(func() { _ = stream.Close() })

	decoded, err := io.ReadAll(stream)
	require.NoError(t, err)

	// Both streams arrive as plain text with no framing bytes left in.
	assert.Equal(t, string(stdout)+string(stderr), string(decoded))
}

// A refused log follow is an error, and the response body is closed rather
// than leaked, because a controller follows logs for every worker it starts.
func TestDaemonClientRefusesANonOKLogStream(t *testing.T) {
	t.Parallel()

	_, socketPath := newFakeEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client, err := docker.NewDaemonClient(socketPath)
	require.NoError(t, err)

	stream, err := client.StreamLogs(context.Background(), "abc123")
	require.Error(t, err)
	assert.Nil(t, stream)
}
