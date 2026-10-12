package peen_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionReplayAndSnapshotOwnership(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver(elelemtest.Text("stored reply"))),
		Model:  elelem.Model{ID: testModelID},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	session := runtime.Session(details.ID)
	_, err = session.Send(context.Background(), peen.MessageRequest{Message: "store this"}, nil)
	require.NoError(t, err)
	turns, err := session.Turns(context.Background(), peen.PageRequest{})
	require.NoError(t, err)
	require.Len(t, turns.Turns, 1)
	turn := turns.Turns[0]
	require.NotNil(t, turn.ContextSnapshotHash)
	require.NotNil(t, turn.PromptSnapshotHash)
	contextSnapshot, err := session.ContextSnapshot(context.Background(), *turn.ContextSnapshotHash)
	require.NoError(t, err)
	assert.NotEmpty(t, contextSnapshot.Manifest)
	prompt, err := session.PromptSnapshot(context.Background(), *turn.PromptSnapshotHash)
	require.NoError(t, err)
	assert.NotEmpty(t, prompt.EffectivePrompt)
	runs, err := session.ModelRuns(context.Background(), peen.PageRequest{})
	require.NoError(t, err)
	require.Len(t, runs.ModelRuns, 1)
	calls, err := session.ModelRunCalls(context.Background(), runs.ModelRuns[0].ID, peen.PageRequest{})
	require.NoError(t, err)
	require.Len(t, calls.Calls, 1)
	assert.Equal(t, session.ID(), calls.Calls[0].SessionID)
	other, err := runtime.OpenSession(context.Background(), peen.OpenSessionOptions{Workspace: t.TempDir()})
	require.NoError(t, err)
	_, err = other.ContextSnapshot(context.Background(), *turn.ContextSnapshotHash)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = other.PromptSnapshot(context.Background(), *turn.PromptSnapshotHash)
	require.ErrorIs(t, err, commerr.ErrNotFound)
	_, err = other.ModelRunCalls(context.Background(), runs.ModelRuns[0].ID, peen.PageRequest{})
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestSessionReplayPaginationWalk(t *testing.T) {
	t.Parallel()
	const recordCount = 5
	const pageLimit = 2
	options := validOptions(t)
	options.Workspace = t.TempDir()
	script := make([]elelemtest.Turn, recordCount)
	for index := range script {
		script[index] = elelemtest.Text(fmt.Sprintf("reply %d", index))
	}
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver(script...)),
		Model:  elelem.Model{ID: testModelID},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	details, err := runtime.Details(context.Background())
	require.NoError(t, err)
	session := runtime.Session(details.ID)
	expectedNotices := make([]string, 0, recordCount)
	for index := range recordCount {
		_, err := session.Send(context.Background(), peen.MessageRequest{
			Message: fmt.Sprintf("request %d", index),
		}, nil)
		require.NoError(t, err)
		notice, err := session.PublishEvent(context.Background(), peen.NoticeRequest{
			Type: "app.report", Summary: "queued report", Delivery: peen.DeliveryQueue,
		})
		require.NoError(t, err)
		expectedNotices = append(expectedNotices, notice.ID)
	}
	testCases := []struct {
		name string
		read func(peen.PageRequest) ([]string, bool, error)
	}{
		{name: "turns", read: func(page peen.PageRequest) ([]string, bool, error) {
			value, err := session.Turns(context.Background(), page)
			ids := make([]string, 0, len(value.Turns))
			for _, item := range value.Turns {
				ids = append(ids, item.ID)
			}

			return ids, value.HasMore, err
		}},
		{name: "model runs", read: func(page peen.PageRequest) ([]string, bool, error) {
			value, err := session.ModelRuns(context.Background(), page)
			ids := make([]string, 0, len(value.ModelRuns))
			for _, item := range value.ModelRuns {
				ids = append(ids, item.ID)
			}

			return ids, value.HasMore, err
		}},
		{name: "notices", read: func(page peen.PageRequest) ([]string, bool, error) {
			value, err := session.Notices(context.Background(), page)
			ids := make([]string, 0, len(value.Notices))
			for _, item := range value.Notices {
				ids = append(ids, item.ID)
			}

			return ids, value.HasMore, err
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seen := make(map[string]bool)
			for _, offset := range []int{0, 2, 4, 5, 100} {
				ids, hasMore, err := tc.read(peen.PageRequest{Limit: pageLimit, Offset: offset})
				require.NoError(t, err)
				assert.Len(t, ids, max(0, min(pageLimit, recordCount-offset)))
				assert.Equal(t, offset+pageLimit < recordCount, hasMore)
				for _, id := range ids {
					assert.False(t, seen[id], "duplicate durable record")
					seen[id] = true
				}
			}
			assert.Len(t, seen, recordCount)
			if tc.name == "notices" {
				for _, id := range expectedNotices {
					assert.True(t, seen[id])
				}
			}
		})
	}
}
