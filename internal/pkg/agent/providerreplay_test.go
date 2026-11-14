package agent

import (
	"context"
	"io"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderReplayCancellationAndCleanupFailure(t *testing.T) {
	t.Parallel()

	for _, cancelRetry := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup failure", true: "cancel"}[cancelRetry],
			func(t *testing.T) {
				t.Parallel()

				baseURL, requests := cutStreamServer(t,
					cutStreamChunk("partial", ""))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				request := elelem.NewRequest(elelem.New(cutStreamDriver(t, baseURL))).
					WithModel(elelem.Model{ID: cutStreamModelID}).
					WithPrompt(elelem.NewPrompt().UserText("hello")).
					WithStreaming(true)
				var retries, closes int
				_, err := runProviderReplay(ctx, request, 0,
					func(context.Context, elelem.RetryAttempt) error {
						retries++
						cancel()

						return nil
					}, func(context.Context) error {
						closes++
						if cancelRetry {
							return nil
						}

						return io.ErrClosedPipe
					})
				if cancelRetry {
					require.ErrorIs(t, err, context.Canceled)
					assert.Equal(t, 1, retries)
				} else {
					require.ErrorIs(t, err, io.ErrClosedPipe)
					assert.Zero(t, retries)
				}

				assert.Equal(t, 1, closes)
				assert.EqualValues(t, 1, requests.Load())
			})
	}
}

func TestProviderReplayChildKeepsOnlySuccessfulAssistant(t *testing.T) {
	t.Parallel()

	baseURL, requests := cutStreamServer(t, cutStreamChunk("partial", ""),
		cutStreamChunk("complete", "stop")+"data: [DONE]\n\n")
	fixture, input, sink := childModelFixture(t)
	registry, err := NewStaticRegistry(map[string]ModelClient{
		runtimeTestModelReference: {
			Client: elelem.New(cutStreamDriver(t, baseURL)),
			Model:  elelem.Model{ID: runtimeTestModelID},
		},
	})
	require.NoError(t, err)
	fixture.base.runtime.models = registry
	var retries int
	sink.liveSink = func(event Event) error {
		if event.Type == EventTypeAgentRunProviderRetry {
			retries++
		}

		return nil
	}
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "complete", result.Output.Text)
	assert.EqualValues(t, 2, requests.Load())
	assert.Equal(t, 1, retries)
	history, err := fixture.base.store.AgentRunHistory(t.Context(),
		sink.sessionID, sink.run.ID)
	require.NoError(t, err)
	require.Len(t, history.Messages, 2)
	assert.Equal(t, "complete", history.Messages[1].Content)
	assert.False(t, history.Messages[1].Incomplete)
	runs, err := fixture.base.store.ListModelRuns(t.Context(), sink.sessionID,
		session.ListModelRunsOptions{})
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	calls, err := fixture.base.store.ListModelCalls(t.Context(), sink.sessionID,
		runs.Items[0].ID, session.ListModelCallsOptions{})
	require.NoError(t, err)
	require.Len(t, calls.Items, 1)
	assert.EqualValues(t, 2, calls.Items[0].TotalAttempts)
}
