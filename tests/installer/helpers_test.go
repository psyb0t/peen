//go:build installer

package installer_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	commandTimeout = 10 * time.Minute
	healthTimeout  = 90 * time.Second
	turnTimeout    = 3 * time.Minute
	stopTimeout    = 60 * time.Second
	pollInterval   = 250 * time.Millisecond

	testUser     = "peentest"
	apiToken     = "installer-test-api-token"
	basePath     = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	wrapperMark  = "# peen-managed-command"
	installLabel = "peen.install"
	sessionLabel = "peen.session"
	controlSpace = "control"
	dockerSpace  = "docker"
	hostSpace    = "host"
	dockerAgent  = "docker:default"

	statusStopped = 3

	// maxDefaultSocketRootBytes is the longest worker socket root whose
	// sockets fit a Unix socket address: 107 bytes, less the 49 that a session
	// directory and the socket file add.
	maxDefaultSocketRootBytes = 58

	webSocketProtocol     = "peen.v1"
	webSocketBearerPrefix = "peen.bearer." //nolint:gosec // A subprotocol prefix, not a credential.
	webSocketMessageSend  = "message.send"
	webSocketCompleted    = "message.completed"
	webSocketFailed       = "message.failed"
	webSocketSessionKey   = "sessionId"
)

var sessionURLPattern = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/\?session=([0-9a-f-]{36})`)

func removeSessionWorkers(ctx context.Context, t *testing.T, session string) {
	t.Helper()

	ids, err := docker(ctx, "ps", "-aq", "--filter", "label="+sessionLabel+"="+session)
	if err != nil || ids == "" {
		return
	}
	for id := range strings.FieldsSeq(ids) {
		if _, err := docker(ctx, "rm", "-f", id); err != nil {
			t.Errorf("remove worker %s: %v", id, err)
		}
	}
}

// startRunnerContainer starts a throwaway container from the suite image with
// the repository at its literal path and the host network, so a controller in
// it is reachable on loopback and the registry proxy is reachable from it.
func startRunnerContainer(t *testing.T, name string, readOnlyRepository bool, user string) runner {
	t.Helper()

	mount := suite.repoRoot + ":" + suite.repoRoot
	if readOnlyRepository {
		mount += ":ro"
	}
	_, err := docker(
		t.Context(),
		"run", "-d", "--init",
		"--name", name,
		"--network", "host",
		"--user", user,
		"-v", mount,
		"--entrypoint", "sleep",
		suite.image, "infinity",
	)
	require.NoError(t, err)

	return runner{container: name}
}

// freePortPair returns two different free ports. Both listeners stay open
// until both ports are known, because the kernel can hand a closed port
// straight back.
func freePortPair(t *testing.T) (int, int) {
	t.Helper()

	first, firstPort := listenOnFreePort(t)
	second, secondPort := listenOnFreePort(t)
	require.NoError(t, first.Close())
	require.NoError(t, second.Close())

	return firstPort, secondPort
}

func listenOnFreePort(t *testing.T) (net.Listener, int) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return listener, address.Port
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)

	return info.Mode().Perm()
}

func requireManagedWrapper(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.SplitN(string(content), "\n", 3)
	require.GreaterOrEqual(t, len(lines), 2)
	assert.Equal(t, wrapperMark, lines[1])
	assert.Equal(t, os.FileMode(0o755), fileMode(t, path))

	return string(content)
}

func waitHealthy(t *testing.T, instance *install) {
	t.Helper()

	require.Eventually(t, func() bool {
		return healthStatus(t, instance) == http.StatusOK
	}, healthTimeout, pollInterval, "%s/healthz never answered", instance.baseURL())
}

func healthStatus(t *testing.T, instance *install) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, instance.baseURL()+"/healthz", nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0
	}
	closeBody(response)

	return response.StatusCode
}

func apiCall(t *testing.T, instance *install, method, path string, body any) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, instance.baseURL()+path, reader)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+apiToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer closeBody(response)
	content, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	return response.StatusCode, content
}

func defaultAgentSpace(t *testing.T, instance *install) string {
	t.Helper()

	status, body := apiCall(t, instance, http.MethodGet, "/api/v1/agent-spaces", nil)
	require.Equal(t, http.StatusOK, status, string(body))
	var spaces struct {
		Default string `json:"default"`
	}
	require.NoError(t, json.Unmarshal(body, &spaces))

	return spaces.Default
}

// sessionAgentSpace reopens a workspace through the API and returns the agent
// space and ID of its session.
func sessionAgentSpace(t *testing.T, instance *install, workspace string) (string, string) {
	t.Helper()

	status, body := apiCall(t, instance, http.MethodPost, "/api/v1/sessions/open", map[string]string{"workspace": workspace})
	require.Equal(t, http.StatusOK, status, string(body))
	var opened struct {
		Session struct {
			ID         string `json:"id"`
			AgentSpace string `json:"agentSpace"`
		} `json:"session"`
		Created bool `json:"created"`
	}
	require.NoError(t, json.Unmarshal(body, &opened))
	assert.False(t, opened.Created, "the wrapper should have created the session")

	return opened.Session.AgentSpace, opened.Session.ID
}

// openedSession runs a peen open form and returns the session ID from the URL
// it printed.
func openedSession(t *testing.T, instance *install, outcome result) string {
	t.Helper()

	require.Zero(t, outcome.code, outcome.output())
	match := sessionURLPattern.FindStringSubmatch(outcome.stdout)
	require.NotNil(t, match, "no session URL in:\n%s", outcome.stdout)
	assert.Equal(t, strconv.Itoa(instance.listenPort), match[1])
	instance.recordSession(match[2])

	return match[2]
}

// sendTurn runs one real turn over the WebSocket and waits for it to complete.
func sendTurn(t *testing.T, instance *install, session string) {
	t.Helper()

	before, err := suite.provider.completions(t.Context())
	require.NoError(t, err)
	dialer := websocket.Dialer{
		HandshakeTimeout: healthTimeout,
		Subprotocols: []string{
			webSocketProtocol,
			webSocketBearerPrefix + base64.RawURLEncoding.EncodeToString([]byte(apiToken)),
		},
	}
	connection, response, err := dialer.DialContext(
		t.Context(),
		"ws://"+instance.listen()+"/ws/v1",
		nil,
	)
	if response != nil {
		closeBody(response)
	}
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()

	event := dabluveees.NewEvent(
		webSocketMessageSend,
		map[string]string{"message": "say something"},
	).SetMetadata(webSocketSessionKey, session)
	require.NoError(t, connection.WriteJSON(event))

	deadline := time.Now().Add(turnTimeout)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))
		received := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&received))
		switch string(received.Type) {
		case webSocketCompleted:
			after, err := suite.provider.completions(t.Context())
			require.NoError(t, err)
			assert.Greater(t, after, before, "the turn must reach the mock provider")

			return
		case webSocketFailed:
			require.FailNow(t, "the turn failed", "%s", received.Data)
		}
	}
}

// containerInspect holds the fields of `docker inspect` the tests check.
type containerInspect struct {
	ID     string `json:"Id"` //nolint:tagliatelle // Docker's inspect JSON names it.
	Config struct {
		User   string            `json:"User"`   //nolint:tagliatelle // Docker's inspect JSON names it.
		Image  string            `json:"Image"`  //nolint:tagliatelle // Docker's inspect JSON names it.
		Labels map[string]string `json:"Labels"` //nolint:tagliatelle // Docker's inspect JSON names it.
	} `json:"Config"` //nolint:tagliatelle // Docker's inspect JSON names it.
	HostConfig struct {
		Binds        []string `json:"Binds"`      //nolint:tagliatelle // Docker's inspect JSON names it.
		GroupAdd     []string `json:"GroupAdd"`   //nolint:tagliatelle // Docker's inspect JSON names it.
		Privileged   bool     `json:"Privileged"` //nolint:tagliatelle // Docker's inspect JSON names it.
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`   //nolint:tagliatelle // Docker's inspect JSON names it.
			HostPort string `json:"HostPort"` //nolint:tagliatelle // Docker's inspect JSON names it.
		} `json:"PortBindings"` //nolint:tagliatelle // Docker's inspect JSON names it.
		RestartPolicy struct {
			Name string `json:"Name"` //nolint:tagliatelle // Docker's inspect JSON names it.
		} `json:"RestartPolicy"` //nolint:tagliatelle // Docker's inspect JSON names it.
	} `json:"HostConfig"` //nolint:tagliatelle // Docker's inspect JSON names it.
}

func inspectContainer(t *testing.T, name string) (containerInspect, bool) {
	t.Helper()

	output, err := docker(t.Context(), "container", "inspect", name)
	if err != nil {
		return containerInspect{}, false
	}
	var inspected []containerInspect
	require.NoError(t, json.Unmarshal([]byte(output), &inspected))
	require.Len(t, inspected, 1)

	return inspected[0], true
}

func imagePresent(t *testing.T, reference string) bool {
	t.Helper()

	_, err := docker(t.Context(), "image", "inspect", reference)

	return err == nil
}

func sessionWorkerImages(t *testing.T, session string) []string {
	t.Helper()

	output, err := docker(t.Context(), "ps", "-a", "--filter", "label="+sessionLabel+"="+session, "--format", "{{.Image}}")
	require.NoError(t, err)

	return strings.Fields(output)
}

func waitExit(t *testing.T, command *exec.Cmd) int {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		exitError := &exec.ExitError{}
		require.True(t, errors.As(err, &exitError), err)

		return exitError.ExitCode()
	case <-time.After(stopTimeout):
		require.FailNow(t, "the foreground controller did not stop")
	}

	return -1
}
