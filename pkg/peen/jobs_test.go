package peen_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/psyb0t/commander"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingRunner struct {
	commander.Commander
	directory   string
	starts      atomic.Int32
	mutex       sync.Mutex
	directories []string
}

//nolint:ireturn // commander.Commander defines the process handle contract.
func (r *recordingRunner) Start(
	ctx context.Context,
	name string,
	args []string,
	options ...commander.Option,
) (commander.Process, error) {
	r.starts.Add(1)
	configuration := &commander.Options{}
	for _, option := range options {
		option(configuration)
	}
	r.mutex.Lock()
	r.directories = append(r.directories, configuration.Dir)
	r.mutex.Unlock()
	options = append(options, commander.WithDir(r.directory))
	process, err := r.Commander.Start(ctx, name, args, options...)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "start test process backend")
	}

	return process, nil
}

func TestCommandRunnerWiresHooksAndJobsWithOwnership(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	directory := t.TempDir()
	root, err := peen.RootFS(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	options.Filesystem = root
	options.WorkspaceID = "runner-workspace"
	runner := &recordingRunner{Commander: commander.New(), directory: directory}
	options.CommandRunner = runner
	writeTestFile(t, filepath.Join(options.ConfigDirectory, ".agents", "hooks.yaml"),
		"version: 1\npreUserMessage:\n  - actions:\n      - type: command\n        command: printf\n        args: ['%s', '{\"decision\":\"allow\"}']\n")
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver(
			elelemtest.ToolCall("command", "runCommand", `{"command":"printf runner-output","purpose":"verify runner"}`),
			elelemtest.Text("done"),
		)),
		Model: elelem.Model{ID: testModelID},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	session := runtime.Session(details.ID)
	_, err = session.Send(context.Background(), peen.MessageRequest{Message: "run"}, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), runner.starts.Load())
	runner.mutex.Lock()
	assert.Equal(t, []string{".", "."}, runner.directories)
	runner.mutex.Unlock()
	jobs, err := session.Jobs().List(context.Background(), peen.JobListRequest{
		State: peen.JobStateExited,
	})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 1)
	jobID := jobs.Jobs[0].JobID
	output, err := session.Jobs().Output(context.Background(), jobID,
		peen.JobOutputRequest{Stream: peen.JobStreamStdout})
	require.NoError(t, err)
	require.Len(t, output.Lines, 1)
	assert.Equal(t, "runner-output", output.Lines[0].Content)
	_, err = session.Jobs().Signal(context.Background(), jobID, peen.JobSignalStop)
	require.NoError(t, err)
	signals, err := session.Jobs().Signals(context.Background(), jobID, peen.PageRequest{})
	require.NoError(t, err)
	require.Len(t, signals.SignalRequests, 1)
	assert.False(t, signals.SignalRequests[0].Accepted)
	other, err := runtime.OpenSession(context.Background(), peen.OpenSessionOptions{Workspace: "other-runner-workspace"})
	require.NoError(t, err)
	_, err = other.Jobs().Output(context.Background(), jobID, peen.JobOutputRequest{})
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = other.Jobs().Signal(context.Background(), jobID, "kill")
	require.ErrorIs(t, err, commerr.ErrNotFound)
}
