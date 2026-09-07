package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/require"
)

// The plan lists message bytes as its own bounded default. The blanket HTTP
// body cap is not it: the runtime is also reachable from an embedding Go
// caller, which never passes through that middleware.
func TestRuntimeRejectsAnOversizedMessage(t *testing.T) {
	const bound = 16

	fixture := newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(),
		func(o *RuntimeOptions) { o.MaxMessageBytes = bound },
	)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   strings.Repeat("m", bound+1),
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, ErrMessageTooLarge)
}

func TestRuntimeAcceptsAMessageAtTheBound(t *testing.T) {
	const bound = 16

	fixture := newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
		func(o *RuntimeOptions) { o.MaxMessageBytes = bound },
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   strings.Repeat("m", bound),
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	require.Equal(t, "done", result.Text)
}
