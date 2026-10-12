package peen_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionsKeepIndependentTranscripts(t *testing.T) {
	runtime := newTestRuntime(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("one"), elelemtest.Text("two"),
	))
	ctx := context.Background()
	firstDirectory := t.TempDir()
	secondDirectory := t.TempDir()
	first, err := runtime.OpenSession(ctx,
		peen.OpenSessionOptions{Workspace: firstDirectory})
	require.NoError(t, err)
	second, err := runtime.OpenSession(ctx,
		peen.OpenSessionOptions{Workspace: secondDirectory})
	require.NoError(t, err)
	assert.NotEqual(t, first.ID(), second.ID())
	_, err = first.Send(ctx, peen.MessageRequest{Message: "first"}, nil)
	require.NoError(t, err)
	_, err = second.Send(ctx, peen.MessageRequest{Message: "second"}, nil)
	require.NoError(t, err)
	page, err := first.Messages(ctx, peen.ListMessagesRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "one"}, messageContents(page.Items))
	page, err = second.Messages(ctx, peen.ListMessagesRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"second", "two"}, messageContents(page.Items))
	resumed, err := runtime.ResumeSession(ctx, first.ID())
	require.NoError(t, err)
	assert.Equal(t, first.ID(), resumed.ID())
	reopened, err := runtime.OpenSession(ctx,
		peen.OpenSessionOptions{Workspace: firstDirectory})
	require.NoError(t, err)
	assert.Equal(t, first.ID(), reopened.ID())
	_, err = runtime.ResumeSession(ctx, testMalformedID)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestSessionPaginationWalk(t *testing.T) {
	runtime := newTestRuntime(t, elelemtest.NewScriptedDriver())
	ctx := context.Background()
	const count = 6
	for range count {
		_, err := runtime.OpenSession(ctx,
			peen.OpenSessionOptions{Workspace: t.TempDir()})
		require.NoError(t, err)
	}
	seen := map[string]bool{}
	for _, offset := range []int{0, 3, 6, 9} {
		page, err := runtime.ListSessions(ctx,
			peen.PageRequest{Limit: 3, Offset: offset})
		require.NoError(t, err)
		for _, item := range page.Items {
			assert.False(t, seen[item.ID])
			seen[item.ID] = true
		}
		assert.Equal(t, offset < count, page.HasMore)
	}
	assert.Len(t, seen, count+1)
}

func TestNewUsesExplicitWorkspaceAndSeparateState(t *testing.T) {
	options := validOptions(t)
	options.Workspace = t.TempDir()
	options.StateDirectory = t.TempDir()
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	assert.Equal(t, options.Workspace, details.Workspace)
	_, err = os.Stat(filepath.Join(options.StateDirectory, "peen.db"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(options.ConfigDirectory, "peen.db"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestNewRefusesOverlappingState(t *testing.T) {
	options := validOptions(t)
	options.StateDirectory = options.ConfigDirectory
	_, err := peen.New(options)
	require.Error(t, err)
	options.StateDirectory = filepath.Join(options.ConfigDirectory, "state")
	_, err = peen.New(options)
	require.Error(t, err)
}
