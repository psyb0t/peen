//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client that tracks the sequence of every frame it applies, disconnects
// mid-turn, and reconnects later catches up exactly what it missed through
// GET /api/v1/session/feed: no gap, no duplicate, and what it replays matches
// what a client that stayed connected the whole time actually saw live.
func TestAPIWebSocketReconnectCatchesUpFromFeed(t *testing.T) {
	workspace := openFreshWorkspace(
		t,
		apiTestWebSocketReconnectWorkspacePrefix,
		nil,
	)
	connection := dialAPIWebSocket(t, &workspace.sessionID)
	observer := openSessionConnection(t, workspace.sessionID)

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	sendSessionMessage(t, connection, workspace.sessionID,
		apiTestWebSocketReconnectMessage)
	awaitAPIWebSocketProviderHold(t, hold)

	// The client that is about to drop reads up to turn.started, which is the
	// last frame it will ever apply live before the gap.
	preDisconnect, _ := readAPISequencedFramesUntil(
		t,
		connection,
		apiTestWebSocketTurnStarted,
	)
	require.NotEmpty(t, preDisconnect)
	for _, frame := range preDisconnect {
		assert.Positive(t, frame.Sequence)
	}
	lastAppliedBeforeDrop := preDisconnect[len(preDisconnect)-1].Sequence

	require.NoError(t, connection.Close())

	hold.Release()

	// The observer never disconnects, so its stream is the live truth a
	// replayed page must agree with wherever their sequences overlap.
	liveFrames, completedEvent := readAPISequencedFramesUntil(
		t,
		observer,
		apiTestWebSocketMessageCompleted,
	)
	require.NotNil(t, completedEvent.Metadata)
	_, completedHasSequence := completedEvent.Metadata.Get(
		apiTestWebSocketMetadataSequence,
	)
	assert.False(t, completedHasSequence,
		"message.completed must carry no feed sequence")

	liveBySequence := make(map[int64]apiTestSequencedFrame, len(liveFrames))
	for _, frame := range liveFrames {
		liveBySequence[frame.Sequence] = frame
	}

	replayed := collectSessionFeed(t, workspace.sessionID, lastAppliedBeforeDrop)
	require.NotEmpty(t, replayed)

	sequences := make([]int64, 0, len(preDisconnect)+len(replayed))
	for _, frame := range preDisconnect {
		sequences = append(sequences, frame.Sequence)
	}
	for _, item := range replayed {
		sequences = append(sequences, item.Sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })

	head := getSession(t, workspace.sessionID).FeedSequence
	require.Len(t, sequences, int(head),
		"the union of live and replayed frames must cover the feed with no gaps or duplicates")
	for index, sequence := range sequences {
		assert.EqualValues(t, index+1, sequence)
	}

	for _, item := range replayed {
		live, found := liveBySequence[item.Sequence]
		if !found {
			continue
		}

		assert.Equal(t, live.Type, item.Type, "sequence %d", item.Sequence)
		assert.Equal(t, live.ID, item.Id, "sequence %d", item.Sequence)

		var liveData any
		if len(live.Data) > 0 {
			require.NoError(t, json.Unmarshal(live.Data, &liveData))
		}
		assert.Equal(t, liveData, item.Data, "sequence %d", item.Sequence)
	}
}

// Params and auth around GET /api/v1/session/feed, proven against the real
// image rather than a mocked runtime.
func TestAPIListSessionFeedValidatesParamsAndAuth(t *testing.T) {
	workspace := openFreshWorkspace(
		t,
		apiTestWebSocketFeedValidationWorkspacePrefix,
		nil,
	)
	sessionID := workspace.sessionID
	unknownSessionID := uuid.New()

	t.Run("negative cursor is rejected", func(t *testing.T) {
		response := apiRequest(t, http.MethodGet, sessionFeedPath+"?cursor=-1",
			nil, withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()))
		assert.Equal(t, http.StatusBadRequest, response.StatusCode)
		assertErrorCode(t, response, aichteeteapee.ErrorCodeValidationFailed)
	})

	t.Run("zero limit is rejected", func(t *testing.T) {
		response := apiRequest(t, http.MethodGet, sessionFeedPath+"?limit=0",
			nil, withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()))
		assert.Equal(t, http.StatusBadRequest, response.StatusCode)
		assertErrorCode(t, response, aichteeteapee.ErrorCodeValidationFailed)
	})

	t.Run("a limit over the maximum is rejected", func(t *testing.T) {
		response := apiRequest(t, http.MethodGet, sessionFeedPath+"?limit=201",
			nil, withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()))
		assert.Equal(t, http.StatusBadRequest, response.StatusCode)
		assertErrorCode(t, response, aichteeteapee.ErrorCodeValidationFailed)
	})

	t.Run("unknown session is reported", func(t *testing.T) {
		response := apiRequest(t, http.MethodGet, sessionFeedPath,
			nil, withHeader(authenticatedHeaders(), headerSessionID, unknownSessionID.String()))
		assert.Equal(t, http.StatusNotFound, response.StatusCode)
		assertErrorCode(t, response, server.ErrorCodeSessionNotFound)
	})

	t.Run("a cursor at the head returns an empty page", func(t *testing.T) {
		head := getSession(t, sessionID).FeedSequence
		page := listSessionFeed(t, sessionID, head, apiTestFeedDefaultLimit)
		assert.Empty(t, page.Items)
		assert.False(t, page.HasMore)
		assert.Equal(t, head, page.NextCursor)
		assert.Equal(t, head, page.Head)
	})

	t.Run("unauthenticated is refused", func(t *testing.T) {
		response := apiRequest(t, http.MethodGet, sessionFeedPath,
			nil, map[string]string{headerSessionID: sessionID.String()})
		assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
		assertErrorCode(t, response, aichteeteapee.ErrorCodeUnauthorized)
	})
}

// A notice recorded through the REST endpoint reaches every connected socket
// live as a sequenced notice.created frame carrying the stored notice, and
// the same row plays back from the feed.
func TestAPINoticeCreatedArrivesLiveAndInTheFeed(t *testing.T) {
	workspace := openFreshWorkspace(
		t,
		apiTestWebSocketNoticeFeedWorkspacePrefix,
		nil,
	)
	sessionID := workspace.sessionID
	connection := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	body, err := json.Marshal(map[string]any{
		"type":    apiTestNoticeFeedType,
		"summary": apiTestNoticeFeedSummary,
	})
	require.NoError(t, err)

	response := apiRequest(
		t,
		http.MethodPost,
		sessionNoticesPath,
		body,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusAccepted)

	live := awaitAPIWebSocketEventOfType(t, connection, apiTestFrameNoticeCreated)
	sequence, hasSequence := apiTestFrameSequence(live)
	require.True(t, hasSequence)
	assert.Positive(t, sequence)

	var notice api.SessionNotice
	require.NoError(t, json.Unmarshal(live.Data, &notice))
	assert.Equal(t, apiTestNoticeFeedType, notice.Type)
	assert.Equal(t, apiTestNoticeFeedSummary, notice.Summary)

	page := listSessionFeed(t, sessionID, sequence-1, 1)
	require.Len(t, page.Items, 1)
	assert.Equal(t, apiTestFrameNoticeCreated, page.Items[0].Type)
	assert.Equal(t, sequence, page.Items[0].Sequence)
	assert.Equal(t, live.ID, page.Items[0].Id)
}

// apiTestSequencedFrame is one live frame captured for feed-catchup
// comparisons: a sequence, the frame's type, its decoded data, and the id of
// the stored record it carries.
type apiTestSequencedFrame struct {
	Sequence int64
	Type     string
	Data     json.RawMessage
	ID       uuid.UUID
}

// readAPISequencedFramesUntil reads frames off connection, collecting every
// one that carries a feed sequence, until it sees stopType (inclusive). A
// message.failed fails the test. It returns the collected frames and the
// stopType event itself, so a caller can inspect an unsequenced frame such as
// message.completed.
func readAPISequencedFramesUntil(
	t *testing.T,
	connection *websocket.Conn,
	stopType string,
) ([]apiTestSequencedFrame, dabluveees.Event) {
	t.Helper()

	frames := make([]apiTestSequencedFrame, 0)

	for {
		event := readWebSocketEvent(t, connection)
		if string(event.Type) == apiTestWebSocketMessageFailed {
			t.Fatalf("WebSocket message failed: %s", event.Data)
		}

		if sequence, ok := apiTestFrameSequence(event); ok {
			frames = append(frames, apiTestSequencedFrame{
				Sequence: sequence,
				Type:     string(event.Type),
				Data:     event.Data,
				ID:       event.ID,
			})
		}

		if string(event.Type) == stopType {
			return frames, event
		}
	}
}

// apiTestFrameSequence reads metadata.sequence off a live frame, when
// present. message.completed and message.failed carry none.
func apiTestFrameSequence(event dabluveees.Event) (int64, bool) {
	if event.Metadata == nil {
		return 0, false
	}

	value, found := event.Metadata.Get(apiTestWebSocketMetadataSequence)
	if !found {
		return 0, false
	}

	number, ok := value.(float64)
	if !ok {
		return 0, false
	}

	return int64(number), true
}

// listSessionFeed reads one page of GET /api/v1/session/feed.
func listSessionFeed(
	t *testing.T,
	sessionID uuid.UUID,
	cursor int64,
	limit int,
) api.FeedPage {
	t.Helper()

	path := sessionFeedPath + "?cursor=" + strconv.FormatInt(cursor, 10) +
		"&limit=" + strconv.Itoa(limit)
	response := apiRequest(
		t,
		http.MethodGet,
		path,
		nil,
		withHeader(authenticatedHeaders(), headerSessionID, sessionID.String()),
	)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.FeedPage](t, response)
}

// collectSessionFeed pages GET /api/v1/session/feed from cursor until hasMore is
// false, returning every item in sequence order.
func collectSessionFeed(
	t *testing.T,
	sessionID uuid.UUID,
	cursor int64,
) []api.FeedFrame {
	t.Helper()

	items := make([]api.FeedFrame, 0)

	for {
		page := listSessionFeed(t, sessionID, cursor, apiTestFeedReplayPageLimit)
		items = append(items, page.Items...)
		cursor = page.NextCursor

		if !page.HasMore {
			return items
		}
	}
}
