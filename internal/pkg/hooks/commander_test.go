package hooks

import (
	"context"
	"testing"

	"github.com/psyb0t/commander"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommanderHookOutputAndLimits(t *testing.T) {
	t.Parallel()
	runner := WithCommander(commander.New())
	output, err := runner(context.Background(), CommandInput{
		Command: "printf", Args: []string{"%s", `{"decision":"allow"}`},
		WorkingDir: t.TempDir(), MaxOutput: 128,
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"decision":"allow"}`, string(output))
	_, err = runner(context.Background(), CommandInput{
		Command: "printf", Args: []string{"%s", "oversized output"},
		WorkingDir: t.TempDir(), MaxOutput: 4,
	})
	require.ErrorIs(t, err, ErrCommandOutputLimit)
}

func TestCommanderHookCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WithCommander(commander.New())(ctx, CommandInput{
		Command: "printf", Args: []string{"%s", "cancelled"},
		WorkingDir: t.TempDir(), MaxOutput: 128,
	})
	require.ErrorIs(t, err, context.Canceled)
}
