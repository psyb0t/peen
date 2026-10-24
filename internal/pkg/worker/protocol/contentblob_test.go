package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/client"
	workflowworker "github.com/psyb0t/go-workflows/worker"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	growingWorkflowRounds      = 40
	growingWorkflowMessageSize = 2048

	// testHistoryPageBytes stands in for the frame limit, so a small history
	// spans many pages.
	testHistoryPageBytes = 16 << 10
)

func growingMessage(round int) string {
	return fmt.Sprintf("round %d: ", round) +
		strings.Repeat("tool output line\n", growingWorkflowMessageSize/17)
}

// growingWorkflowRound appends one long message, the shape of one model and
// tool round adding to a turn's history.
func growingWorkflowRound(_ context.Context, history []string) (
	[]string, error,
) {
	return append(history, growingMessage(len(history))), nil
}

// growingWorkflow passes the whole history through every activity, as the
// turn workflow passes the turn context.
func growingWorkflow(ctx workflow.Context, rounds int) ([]string, error) {
	var history []string

	for range rounds {
		next, err := workflow.ExecuteActivity[[]string](ctx,
			workflow.DefaultActivityOptions, growingWorkflowRound, history,
		).Get(ctx)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "run growing round")
		}

		history = next
	}

	return history, nil
}

// runGrowingWorkflow runs growingWorkflow over the fixture's RPC backend and
// returns its instance once it finished.
func runGrowingWorkflow(
	t *testing.T,
	fixture workflowRPCFixture,
) *workflow.Instance {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	queue := workflow.Queue(fixture.sessionID.String())
	options := workflowworker.DefaultOptions
	options.WorkflowQueues = []workflow.Queue{queue}
	options.ActivityQueues = []workflow.Queue{queue}

	executor := workflowworker.New(fixture.remote, &options)
	require.NoError(t, executor.RegisterWorkflow(growingWorkflow))
	require.NoError(t, executor.RegisterActivity(growingWorkflowRound))
	require.NoError(t, executor.Start(ctx))

	t.Cleanup(sync.OnceFunc(func() {
		cancel()
		require.NoError(t, executor.WaitForCompletion())
	}))

	api := client.New(fixture.remote)
	instance, err := api.CreateWorkflowInstance(ctx,
		client.WorkflowInstanceOptions{
			InstanceID: WorkflowInstanceID(fixture.sessionID, uuid.New()),
			Queue:      queue,
		}, growingWorkflow, growingWorkflowRounds,
	)
	require.NoError(t, err)

	result, err := client.GetWorkflowResult[[]string](t.Context(), api,
		instance, testEventuallyTimeout)
	require.NoError(t, err)
	require.Len(t, result, growingWorkflowRounds)

	for round, message := range result {
		require.Equal(t, growingMessage(round), message)
	}

	return instance
}

// A turn's workflow history used to repeat the whole context in every event,
// growing with the square of the rounds until the history read exceeded the
// worker protocol's frame limit and the turn died. Each long string must now
// be stored once, so the history stays a small part of its inline size.
func TestWorkflowHistoryStoresEachLongStringOnce(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)

	instance := runGrowingWorkflow(t, fixture)

	history, err := fixture.storage.GetWorkflowInstanceHistory(t.Context(),
		instance, nil)
	require.NoError(t, err)

	encoded, err := json.Marshal(history)
	require.NoError(t, err)

	// Inline, every scheduled input and every result carries the history so
	// far: about rounds squared times one message.
	inline := growingWorkflowRounds * growingWorkflowRounds *
		growingWorkflowMessageSize
	assert.Less(t, len(encoded), inline/10,
		"the stored history must not repeat the long strings")
}

// A history larger than one protocol frame used to close the worker
// connection on the read, and with it every turn of the session. The worker
// now reads history in pages. A small page stands in for the frame here, so
// the read spans many pages without a history near 32 MiB.
func TestWorkflowHistoryIsReadInPages(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	fixture.controller.workflows.historyPageBytes = testHistoryPageBytes

	instance := runGrowingWorkflow(t, fixture)

	stored, err := fixture.storage.GetWorkflowInstanceHistory(t.Context(),
		instance, nil)
	require.NoError(t, err)

	encoded, err := json.Marshal(encodeWorkflowEvents(stored))
	require.NoError(t, err)
	require.Greater(t, len(encoded), 4*testHistoryPageBytes,
		"the history must span several pages")

	remote, err := fixture.remote.GetWorkflowInstanceHistory(t.Context(),
		instance, nil)
	require.NoError(t, err)
	require.Len(t, remote, len(stored))

	for index := range stored {
		want, err := json.Marshal(&workflowEvent{Event: stored[index]})
		require.NoError(t, err)

		got, err := json.Marshal(&workflowEvent{Event: remote[index]})
		require.NoError(t, err)

		require.JSONEq(t, string(want), string(got), "event %d", index)
	}

	after := stored[len(stored)/2].SequenceID
	tail, err := fixture.remote.GetWorkflowInstanceHistory(t.Context(),
		instance, &after)
	require.NoError(t, err)
	require.Len(t, tail, len(stored)-len(stored)/2-1,
		"a read after a sequence ID returns only the later events")
	assert.Equal(t, stored[len(stored)/2+1].SequenceID, tail[0].SequenceID)
}

func TestWorkflowHistoryPagesHoldAtLeastOneEvent(t *testing.T) {
	t.Parallel()

	event := func(size int) *workflowEvent {
		return &workflowEvent{Event: &history.Event{
			ID: uuid.NewString(), Type: history.EventType_ActivityScheduled,
			Attributes: &history.ActivityScheduledAttributes{
				Name: strings.Repeat("a", size),
			},
		}}
	}

	page, err := workflowHistoryPageOf([]*workflowEvent{
		event(testHistoryPageBytes + 1), event(10),
	}, testHistoryPageBytes)
	require.NoError(t, err)
	assert.Len(t, page.Events, 1, "an oversized first event is still sent")
	assert.True(t, page.More)

	page, err = workflowHistoryPageOf([]*workflowEvent{event(10), event(10)},
		testHistoryPageBytes)
	require.NoError(t, err)
	assert.Len(t, page.Events, 2)
	assert.False(t, page.More)

	page, err = workflowHistoryPageOf(nil, testHistoryPageBytes)
	require.NoError(t, err)
	assert.Empty(t, page.Events)
	assert.False(t, page.More)
}

// A reply too large for one frame used to close the whole connection. It now
// fails only that call, and the connection keeps serving.
func TestOversizedAnswerFailsOnlyItsCall(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	remote := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)

	var blobs []contentblob.Blob

	for index := range 3 {
		literal := `"` + fmt.Sprint(index) +
			strings.Repeat("y", maxFrameBytes/3) + `"`
		_, compacted := contentblob.Compact([]byte(`[` + literal + `]`))
		require.Len(t, compacted, 1)
		blobs = append(blobs, compacted...)
	}

	for _, blob := range blobs {
		require.NoError(t, fixture.controller.store.PutContentBlobs(
			t.Context(), []contentblob.Blob{blob}))
	}

	hashes := make([]string, 0, len(blobs))
	for _, blob := range blobs {
		hashes = append(hashes, blob.Hash)
	}

	_, err := remote.GetContentBlobs(t.Context(), hashes)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)

	found, err := remote.GetContentBlobs(t.Context(), hashes[:1])
	require.NoError(t, err, "the connection must still serve calls")
	assert.Len(t, found, 1)
}

func TestContentBlobsTravelOverRPCForTheOwnSessionOnly(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	remote := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)

	_, blobs := contentblob.Compact([]byte(`["` +
		strings.Repeat("blob text ", 100) + `"]`))
	require.Len(t, blobs, 1)

	require.NoError(t, remote.PutContentBlobs(t.Context(), blobs))

	found, err := remote.GetContentBlobs(t.Context(), []string{blobs[0].Hash})
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{blobs[0].Hash: blobs[0].Data}, found)

	forged := []contentblob.Blob{{Hash: blobs[0].Hash, Data: []byte(`"x"`)}}
	require.ErrorIs(t, remote.PutContentBlobs(t.Context(), forged),
		commerr.ErrValidationFailed)

	other := NewStore(fixture.remote.conn, uuid.New(),
		fixture.controller.generationID)
	require.ErrorIs(t, other.PutContentBlobs(t.Context(), blobs),
		commerr.ErrPermissionDenied)

	_, err = other.GetContentBlobs(t.Context(), []string{blobs[0].Hash})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}
