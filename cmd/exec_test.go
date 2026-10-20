package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/psyb0t/peen/internal/pkg/oneshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecFlagsBuildRunOptions(t *testing.T) {
	t.Parallel()

	command := execCommand()
	workspace := t.TempDir()
	require.NoError(t, command.ParseFlags([]string{
		"--model", "provider/model", "--reasoning-effort", "high",
		"--system-prompt", "instructions", "--system-prompt-mode", "replace",
		"--agent-space", "docker", "--workspace", workspace,
		"--output", "text", "fix", "tests",
	}))
	readFlag := func(name string) string {
		value, err := command.Flags().GetString(name)
		require.NoError(t, err)

		return value
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	command.SetOut(stdout)
	command.SetErr(stderr)
	options, err := execRunOptions(command, command.Flags().Args(), execOptions{
		model: readFlag(flagModel), reasoningEffort: readFlag(flagReasoningEffort),
		systemPrompt:     readFlag(flagSystemPrompt),
		systemPromptMode: readFlag(flagSystemPromptMode),
		agentSpace:       readFlag(flagAgentSpace),
		workspace:        readFlag(flagWorkspace),
		output:           readFlag(flagOutput),
	})
	require.NoError(t, err)
	assert.Equal(t, workspace, options.Workspace)
	assert.Equal(t, "docker", options.AgentSpace)
	assert.Equal(t, oneshot.OutputFormatText, options.Output)
	assert.Equal(t, "fix tests", options.Message.Message)
	require.NotNil(t, options.Message.Model)
	assert.Equal(t, "provider/model", *options.Message.Model)
	require.NotNil(t, options.Message.ReasoningEffort)
	assert.Equal(t, wsapi.MessageSendReasoningEffortHigh,
		*options.Message.ReasoningEffort)
	require.NotNil(t, options.Message.SystemPrompt)
	assert.Equal(t, "instructions", options.Message.SystemPrompt.Content)
	assert.Equal(t, wsapi.MessageSystemPromptModeReplace,
		options.Message.SystemPrompt.Mode)
	assert.Same(t, stdout, options.Stdout)
	assert.Same(t, stderr, options.Stderr)
}

func TestExecStdinPrompt(t *testing.T) {
	t.Parallel()

	command := execCommand()
	command.SetIn(strings.NewReader("fix the tests\n"))
	options, err := execRunOptions(command, []string{"-"}, execOptions{
		output: "jsonl",
	})
	require.NoError(t, err)
	assert.Equal(t, "fix the tests\n", options.Message.Message)
	assert.True(t, filepath.IsAbs(options.Workspace))
	assert.Nil(t, options.Answers)
}

func TestExecPrompt(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		args      []string
		stdin     string
		fromStdin bool
		want      string
	}{
		{name: "arguments", args: []string{"fix", "tests"}, want: "fix tests"},
		{name: "stdin", stdin: "prompt\n", fromStdin: true, want: "prompt\n"},
		{
			name: "dash among arguments", args: []string{"-", "literal"},
			want: "- literal",
		},
		{name: "empty arguments"},
		{name: "empty stdin", fromStdin: true, stdin: " \n\t"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := execPrompt(strings.NewReader(tc.stdin), tc.args, tc.fromStdin)
			if tc.want == "" {
				require.ErrorIs(t, err, commerr.ErrValidationFailed)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExecPromptFlag(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		prompt  string
		args    []string
		stdin   string
		want    string
		wantErr error
	}{
		{name: "flag", prompt: "fix the tests", want: "fix the tests"},
		{name: "flag reads stdin", prompt: "-", stdin: "piped\n", want: "piped\n"},
		{name: "arguments", args: []string{"fix", "tests"}, want: "fix tests"},
		{
			name: "flag and arguments", prompt: "fix", args: []string{"tests"},
			wantErr: commerr.ErrValidationFailed,
		},
		{name: "neither", wantErr: commerr.ErrValidationFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			command := execCommand()
			command.SetIn(strings.NewReader(tc.stdin))
			options, err := execRunOptions(command, tc.args, execOptions{
				prompt: tc.prompt,
				output: "jsonl",
			})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, options.Message.Message)
		})
	}
}

func TestExecPromptFlagParses(t *testing.T) {
	t.Parallel()

	command := execCommand()
	require.NoError(t, command.ParseFlags([]string{
		"--prompt", "fix the tests", "--output", "text",
	}))
	prompt, err := command.Flags().GetString(flagPrompt)
	require.NoError(t, err)
	assert.Equal(t, "fix the tests", prompt)
	assert.Empty(t, command.Flags().Args())
}

func TestExecPromptReadFailure(t *testing.T) {
	t.Parallel()

	_, err := execPrompt(execFailingReader{}, []string{"-"}, true)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

type execFailingReader struct{}

func (execFailingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestExecMessageRejectsInvalidReasoningEffort(t *testing.T) {
	t.Parallel()

	_, err := execMessage("prompt", execOptions{reasoningEffort: "invalid"})
	require.Error(t, err)
}

func TestExecRunOptionsRejectsInvalidOutput(t *testing.T) {
	t.Parallel()

	_, err := execRunOptions(execCommand(), []string{"prompt"}, execOptions{
		output: "invalid",
	})
	require.ErrorIs(t, err, commerr.ErrInvalidValue)
}

func TestExecExitCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		outcome controlclient.TurnOutcome
		code    int
	}{
		{outcome: controlclient.TurnOutcomeCompleted},
		{outcome: controlclient.TurnOutcomeFailed, code: exitTurnFailed},
		{outcome: controlclient.TurnOutcomeCancelled, code: exitTurnCancelled},
		{code: exitTurnFailed},
	}
	for _, tc := range testCases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			t.Parallel()
			err := execExitCode(controlclient.TurnResult{Outcome: tc.outcome})
			if tc.code == 0 {
				require.NoError(t, err)

				return
			}
			var exitErr exitCodeError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, tc.code, exitErr.code)
		})
	}
}

func TestExecResultCancellationExits130(t *testing.T) {
	t.Parallel()

	err := execResult(controlclient.TurnResult{}, ctxerrors.Wrap(
		context.Canceled, "abandon the run",
	))
	var exitErr exitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitTurnCancelled, exitErr.code)
}

func TestExecResultPreservesRunFailure(t *testing.T) {
	t.Parallel()

	err := execResult(controlclient.TurnResult{}, io.ErrUnexpectedEOF)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	var exitErr exitCodeError
	assert.False(t, errors.As(err, &exitErr))
}

func TestExecResultUnanswerableQuestionUsesCancelledOutcome(t *testing.T) {
	t.Parallel()

	err := execResult(controlclient.TurnResult{
		Outcome: controlclient.TurnOutcomeCancelled,
	}, oneshot.ErrQuestionUnanswerable)
	var exitErr exitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitTurnCancelled, exitErr.code)
}
