//go:build installer

package installer_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

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

	listenPort, metricsPort := freePortPair(t)
	instance := &install{
		id:            id,
		dir:           directory,
		home:          filepath.Join(directory, "h"),
		peenHome:      filepath.Join(directory, "p"),
		root:          filepath.Join(directory, "w"),
		project:       filepath.Join(directory, "w", "proj"),
		outside:       filepath.Join(directory, "o"),
		listenPort:    listenPort,
		metricsPort:   metricsPort,
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
		// A control worker sees the whole repository, so a workspace under
		// .testing inherits this repository's AGENTS.md and rules, which are
		// larger than the budget at the default context size.
		{"PEEN_MAX_SYSTEM_PROMPT_PERCENT", "0"},
	}
	// A deep checkout pushes the default socket root over the limit, which
	// only TestDoctorAndStartExplainATooLongWorkerSocketRoot means to test.
	defaultSocketRoot := filepath.Join(i.peenHome, "state", "workers")
	if len(defaultSocketRoot) > maxDefaultSocketRootBytes {
		settings = append(settings, [2]string{
			"PEEN_WORKER_SOCKET_DIR", filepath.Join(i.dir, "s"),
		})
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
