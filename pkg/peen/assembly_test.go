package peen

import (
	"context"
	"io/fs"
	"os"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/require"
)

func TestMemoryRuntimeRemovesTemporaryState(t *testing.T) {
	t.Parallel()
	runtime, err := New(Options{
		ConfigDirectory: t.TempDir(), Workspace: t.TempDir(),
		RootAgent: "core:general-purpose", InMemoryStore: true,
		DefaultModel: "test/model",
		Models: map[string]ModelClient{
			"test/model": {
				Client: elelem.New(elelemtest.NewScriptedDriver()),
				Model:  elelem.Model{ID: "model"},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	state := runtime.temporaryState
	require.NotEmpty(t, state)
	_, err = os.Stat(state)
	require.NoError(t, err)
	require.NoError(t, runtime.Close(context.Background()))
	_, err = os.Stat(state)
	require.ErrorIs(t, err, fs.ErrNotExist)
}
