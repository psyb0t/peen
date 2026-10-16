//go:build installer

package installer_test

import (
	"bufio"
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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
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

	webSocketProtocol     = "peen.v1"
	webSocketBearerPrefix = "peen.bearer." //nolint:gosec // A subprotocol prefix, not a credential.
	webSocketMessageSend  = "message.send"
	webSocketCompleted    = "message.completed"
	webSocketFailed       = "message.failed"
	webSocketSessionKey   = "sessionId"
)

var sessionURLPattern = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/\?session=([0-9a-f-]{36})`)

// runner starts a command either in this test process's own environment or in
// a throwaway container the test created.
type runner struct {
	container string
	user      string
}

type result struct {
	stdout string
	stderr string
	code   int
}

func (r result) output() string {
	return r.stdout + r.stderr
}

// install is one isolated installation: its own HOME, PEEN_HOME, workspace
// root, listen port, and container name.
type install struct {
	id            string
	dir           string
	home          string
	peenHome      string
	root          string
	project       string
	outside       string
	listenPort    int
	metricsPort   int
	containerName string
	runner        runner
	pathHasBin    bool
	extraEnv      []string
	sessions      []string
}

type installOptions struct {
	runner         runner
	pathWithoutBin bool
}

func newInstall(t *testing.T, options installOptions) *install {
	t.Helper()

	var directory, id string
	for range 32 {
		id = randomHex(3)
		directory = filepath.Join(suite.repoRoot, ".testing", "i"+id)
		if err := os.Mkdir(directory, 0o700); err == nil {
			break
		}
		directory = ""
	}
	require.NotEmpty(t, directory, "no free test directory name")

	instance := &install{
		id:            id,
		dir:           directory,
		home:          filepath.Join(directory, "h"),
		peenHome:      filepath.Join(directory, "p"),
		root:          filepath.Join(directory, "w"),
		project:       filepath.Join(directory, "w", "proj"),
		outside:       filepath.Join(directory, "o"),
		listenPort:    freePort(t),
		metricsPort:   freePort(t),
		containerName: containerNamePrefix + suite.runID + "-" + id,
		runner:        options.runner,
		pathHasBin:    !options.pathWithoutBin,
	}
	for _, path := range []string{instance.home, instance.project, instance.outside} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	t.Cleanup(func() { instance.cleanup(t) })

	return instance
}

func (i *install) wrapper() string {
	return filepath.Join(i.home, ".local", "bin", "peen")
}

func (i *install) binary() string {
	return filepath.Join(i.home, ".local", "lib", "peen", "peen")
}

func (i *install) envFile() string {
	return filepath.Join(i.peenHome, ".env")
}

func (i *install) listen() string {
	return "127.0.0.1:" + strconv.Itoa(i.listenPort)
}

func (i *install) baseURL() string {
	return "http://" + i.listen()
}

func (i *install) environment() []string {
	path := basePath
	if i.pathHasBin {
		path = filepath.Join(i.home, ".local", "bin") + ":" + path
	}
	environment := make([]string, 0, 10+len(i.extraEnv))
	environment = append(environment,
		"HOME="+i.home,
		"USER="+testUser,
		"LOGNAME="+testUser,
		"PATH="+path,
		"LANG=C.UTF-8",
		"PEEN_HOME="+i.peenHome,
		"PEEN_INSTALL_REGISTRY_URL="+suite.proxy.URL,
		"PEEN_INSTALL_SOURCE_URL="+suite.sourceURL,
		"PEEN_INSTALL_LATEST_URL="+suite.latestURL(),
		"PEEN_NO_BROWSER=1",
	)

	return append(environment, i.extraEnv...)
}

func (i *install) command(ctx context.Context, directory string, arguments ...string) *exec.Cmd {
	if i.runner.container == "" {
		//nolint:gosec // The suite runs its own installer and wrapper by design.
		command := exec.CommandContext(ctx, arguments[0], arguments[1:]...)
		command.Env = i.environment()
		command.Dir = directory

		return command
	}
	dockerArguments := []string{"exec", "-i", "-w", directory}
	if i.runner.user != "" {
		dockerArguments = append(dockerArguments, "--user", i.runner.user)
	}
	for _, entry := range i.environment() {
		dockerArguments = append(dockerArguments, "-e", entry)
	}
	dockerArguments = append(dockerArguments, i.runner.container)

	//nolint:gosec // docker exec of the suite's own commands in its own container.
	return exec.CommandContext(ctx, "docker", append(dockerArguments, arguments...)...)
}

func (i *install) run(t *testing.T, directory string, stdin io.Reader, arguments ...string) result {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), commandTimeout)
	defer cancel()
	command := i.command(ctx, directory, arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdin = stdin
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	outcome := result{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		exitError := &exec.ExitError{}
		require.ErrorAs(t, err, &exitError, "run %v: %s", arguments, outcome.output())
		outcome.code = exitError.ExitCode()
	}
	t.Logf("$ %s (exit %d)\n%s", strings.Join(arguments, " "), outcome.code, outcome.output())

	return outcome
}

func (i *install) peen(t *testing.T, arguments ...string) result {
	t.Helper()

	return i.run(t, i.home, nil, append([]string{i.wrapper()}, arguments...)...)
}

func (i *install) peenIn(t *testing.T, directory string, arguments ...string) result {
	t.Helper()

	return i.run(t, directory, nil, append([]string{i.wrapper()}, arguments...)...)
}

func (i *install) mustPeen(t *testing.T, arguments ...string) result {
	t.Helper()

	outcome := i.peen(t, arguments...)
	require.Zero(t, outcome.code, "peen %v failed:\n%s", arguments, outcome.output())

	return outcome
}

func (i *install) installer(t *testing.T, flags ...string) result {
	t.Helper()

	return i.run(t, i.home, nil, append([]string{"bash", filepath.Join(suite.repoRoot, "install.sh")}, flags...)...)
}

// installerPiped feeds install.sh on stdin, as `curl ... | bash -s -- flags`
// does, so the script must never read its own stdin.
func (i *install) installerPiped(t *testing.T, flags ...string) result {
	t.Helper()

	return i.run(t, i.home, bytes.NewReader(suite.installScript), append([]string{"bash", "-s", "--"}, flags...)...)
}

func (i *install) setupFlags(control, agent string) []string {
	return []string{
		"--control-space", control,
		"--agent-space", agent,
		"--workspace-root", i.root,
		"--listen", i.listen(),
		"--container-name", i.containerName,
	}
}

func (i *install) mustInstall(t *testing.T, control, agent string) {
	t.Helper()

	outcome := i.installer(t, i.setupFlags(control, agent)...)
	require.Zero(t, outcome.code, "install failed:\n%s", outcome.output())
}

// configure points the installation at the mock provider and sets a token, so
// every API call below also proves the wrapper sends it.
func (i *install) configure(t *testing.T) {
	t.Helper()

	upstreams, err := json.Marshal([]map[string]string{{
		"name":      providerName,
		"type":      "openai",
		"baseUrl":   suite.provider.url,
		"apiKeyEnv": providerKeyName,
	}})
	require.NoError(t, err)
	settings := [][2]string{
		{"PEEN_UPSTREAMS", string(upstreams)},
		{providerKeyName, providerKey},
		{"PEEN_DEFAULT_MODEL", providerName + "/" + providerModel},
		{"PEEN_COMPACTION_MODEL", providerName + "/" + providerModel},
		{"PEEN_API_TOKEN", apiToken},
		{"PEEN_METRICS_LISTEN_ADDRESS", "127.0.0.1:" + strconv.Itoa(i.metricsPort)},
	}
	for _, setting := range settings {
		i.mustPeen(t, "config", "set", setting[0], setting[1])
	}
}

// readEnv parses .env with Docker --env-file rules, independently of the
// wrapper's own parser.
func (i *install) readEnv(t *testing.T) map[string]string {
	t.Helper()

	content, err := os.ReadFile(i.envFile())
	require.NoError(t, err)
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for scanner.Scan() {
		line := strings.TrimLeft(scanner.Text(), " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			values[key] = value
		}
	}
	require.NoError(t, scanner.Err())

	return values
}

func (i *install) recordSession(id string) {
	i.sessions = append(i.sessions, id)
}

// cleanup removes only what this installation created: its controller
// container when the label names this PEEN_HOME, the foreground run
// container, worker containers of sessions this test opened, a host
// controller whose command line is this installation's binary, and its
// throwaway runner container.
func (i *install) cleanup(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), suiteTeardownTimeout)
	defer cancel()
	for _, name := range []string{i.containerName, i.containerName + "-run"} {
		label, err := docker(ctx, "container", "inspect", "-f", "{{index .Config.Labels \""+installLabel+"\"}}", name)
		if err != nil {
			continue
		}
		if name == i.containerName && label != i.peenHome {
			continue
		}
		if _, err := docker(ctx, "rm", "-f", name); err != nil {
			t.Errorf("remove %s: %v", name, err)
		}
	}
	for _, session := range i.sessions {
		removeSessionWorkers(ctx, t, session)
	}
	if i.runner.container != "" {
		if _, err := docker(ctx, "rm", "-f", i.runner.container); err != nil {
			t.Errorf("remove %s: %v", i.runner.container, err)
		}
	} else {
		i.killHostController(t)
	}
	if t.Failed() {
		t.Logf("kept %s for inspection", i.dir)

		return
	}
	if err := os.RemoveAll(i.dir); err != nil {
		t.Errorf("remove %s: %v", i.dir, err)
	}
}

func (i *install) killHostController(t *testing.T) {
	t.Helper()

	pid, ok := i.hostPID()
	if !ok {
		return
	}
	commandLine, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || !strings.HasPrefix(string(commandLine), i.binary()+"\x00") {
		return
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Errorf("kill pid %d: %v", pid, err)
	}
}

func (i *install) hostPID() (int, bool) {
	content, err := os.ReadFile(filepath.Join(i.peenHome, "state", "controller.pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return 0, false
	}

	return pid, true
}

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

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.NoError(t, listener.Close())

	return address.Port
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

// startForeground starts a command that keeps running, for `peen run`.
func (i *install) startForeground(t *testing.T, arguments ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()

	command := i.command(context.WithoutCancel(t.Context()), i.home, arguments...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	return command, &output
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
