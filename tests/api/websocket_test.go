//go:build integration

package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	server "github.com/psyb0t/peen/internal/pkg/http/server"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// apiTestWebSocketPath is its own literal, not derived from apiBasePath:
	// the WebSocket and the REST API are separate surfaces with separate
	// paths.
	apiTestWebSocketPath               = "/ws/v1"
	apiTestWebSocketSessionIDParameter = "sessionId"
	apiTestWebSocketSubprotocol        = "peen.v1"
	apiTestWebSocketBearerPrefix       = "peen.bearer." //nolint:gosec // A subprotocol prefix, not a credential.
	apiTestWebSocketMessageSend        = "message.send"
	apiTestWebSocketMessageCompleted   = "message.completed"
	apiTestWebSocketMessageFailed      = "message.failed"
	apiTestWebSocketTurnStarted        = "turn.started"
	apiTestWebSocketContentBlockDelta  = "content_block_delta"
	apiTestWebSocketTurnCompleted      = "turn.completed"
	apiTestWebSocketMessageDelivered   = "userMessage.delivered"
	apiTestWebSocketMetadataSessionID  = "sessionId"
	apiTestWebSocketMetadataRequestID  = "requestId"
	apiTestWebSocketMetadataSequence   = "sequence"
	apiTestWebSocketReadTimeout        = 30 * time.Second
	apiTestWebSocketIsolationTimeout   = time.Second
	apiTestWebSocketInitialMessage     = "open a synchronized websocket session"
	apiTestWebSocketActiveMessage      = "hold this websocket turn active"
	apiTestWebSocketQueuedMessage      = "queue this websocket message"
	apiTestWebSocketReasoningMessage   = "think about this websocket message"
	apiTestWebSocketReasoningEffort    = "high"

	apiTestWebSocketSkillMessage        = "plan this with :core:planning"
	apiTestWebSocketUnknownSkillMessage = "run :no-such-skill on this"

	apiTestWebSocketOrderedWorkspacePrefix = "admission-order-"
	apiTestWebSocketOrderedFixtureFile     = "README.md"
	apiTestWebSocketOrderedFixture         = "a workspace no test has opened yet"
	apiTestWebSocketFirstOrderedMessage    = "run the first ordered message"
	apiTestWebSocketSecondOrderedMessage   = "then answer the second ordered message"

	apiTestWebSocketUnattendedMessage             = "finish this turn with nobody attached"
	apiTestWebSocketUnattendedWorkspacePrefix     = "unattended-turn-"
	apiTestWebSocketFeedValidationWorkspacePrefix = "feed-validation-"
	apiTestWebSocketNoticeFeedWorkspacePrefix     = "notice-feed-"

	apiTestWebSocketReconnectMessage         = "catch up on this after a reconnect"
	apiTestWebSocketReconnectWorkspacePrefix = "reconnect-feed-"
	apiTestFrameNoticeCreated                = "notice.created"
	apiTestNoticeFeedType                    = "test.feed.notice"
	apiTestNoticeFeedSummary                 = "feed sequencing fixture notice"
	apiTestFeedReplayPageLimit               = 2
	apiTestFeedDefaultLimit                  = 100
)

type apiTestWebSocketResult struct {
	Queued bool `json:"queued"`
}

// A turn runs to completion even though every socket attached to its session
// closes while the provider request is still in flight. Only explicit Stop,
// turn failure, or a crash stop work now; a disconnect does none of those.
func TestAPIWebSocketTurnFinishesWithNoClientAttached(t *testing.T) {
	workspace := openFreshWorkspace(
		t,
		apiTestWebSocketUnattendedWorkspacePrefix,
		nil,
	)
	sessionID := workspace.sessionID
	connection := dialAPIWebSocket(t, &sessionID)
	observer := dialAPIWebSocket(t, &sessionID)

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	sendSessionMessage(t, connection, sessionID, apiTestWebSocketUnattendedMessage)
	awaitAPIWebSocketProviderHold(t, hold)

	require.NoError(t, connection.Close())
	require.NoError(t, observer.Close())

	assert.Never(t, func() bool {
		select {
		case <-hold.Cancelled:
			return true
		default:
			return false
		}
	}, apiTestWebSocketIsolationTimeout, 10*time.Millisecond,
		"closing every session socket must not cancel the provider request")

	hold.Release()

	require.Eventually(t, func() bool {
		return !getSession(t, sessionID).ActiveTurn
	}, requestTimeout, 10*time.Millisecond)

	messages := collectAllMessages(t, sessionID)
	contents := apiMessageContents(messages)
	assert.Contains(t, contents, apiTestWebSocketUnattendedMessage)
	require.NotEmpty(t, messages)
	lastMessage := messages[len(messages)-1]
	assert.Equal(t, api.MessageRoleAssistant, lastMessage.Role)
	assert.NotEmpty(t, lastMessage.Content)

	feed := collectSessionFeed(t, sessionID, 0)
	feedTypes := make([]string, 0, len(feed))
	for _, item := range feed {
		feedTypes = append(feedTypes, item.Type)
	}
	assert.Contains(t, feedTypes, apiTestWebSocketTurnCompleted)
}

// A client disconnecting from a session with other clients still attached
// must not disturb the turn those other clients see, the same as it would
// not disturb it with nobody attached at all.
func TestAPIWebSocketOneClientDisconnectingDoesNotAffectAnother(t *testing.T) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		logs, err := integrationInfra.AppLogs(ctx)
		require.NoError(t, err)
		t.Logf("continuity failure container logs: %s", logs)
	})
	sessionID := createAPIWebSocketSession(t, "open shared client session")
	first := dialAPIWebSocket(t, &sessionID)
	second := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)
	require.NoError(t, writeAPIWebSocketMessage(t, first,
		"keep running while another session client remains"))
	awaitAPIWebSocketProviderHold(t, hold)
	require.NoError(t, first.Close())
	hold.Release()
	completed := awaitAPIWebSocketCompletion(t, second, false)
	assert.Equal(t, sessionID, completed.sessionID)
	select {
	case <-hold.Cancelled:
		t.Fatal("disconnect cancelled work with another session client present")
	default:
	}
}

type apiTestWebSocketFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiTestWebSocketObservation struct {
	result          apiTestWebSocketResult
	sessionID       uuid.UUID
	requestID       uuid.UUID
	agentEventTypes []string
	// deliveredRequestIDs lists the request IDs of queued messages that
	// reached the model before this completion arrived.
	deliveredRequestIDs []uuid.UUID
}

func TestAPIWebSocketBroadcastsGlobalEventsAndQueues(t *testing.T) {
	sessionID := createAPIWebSocketSession(t, apiTestWebSocketInitialMessage)
	filteredOutSessionID := uuid.New()

	first := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	observer := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, observer.Close()) })
	filteredOther := dialAPIWebSocket(t, &filteredOutSessionID)
	t.Cleanup(func() { require.NoError(t, filteredOther.Close()) })

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	require.NoError(t, writeAPIWebSocketMessage(
		t,
		first,
		apiTestWebSocketActiveMessage,
	))
	awaitAPIWebSocketProviderHold(t, hold)

	require.NoError(t, writeAPIWebSocketMessage(
		t,
		second,
		apiTestWebSocketQueuedMessage,
	))

	firstQueued := awaitAPIWebSocketCompletion(t, first, true)
	secondQueued := awaitAPIWebSocketCompletion(t, second, true)
	assert.Equal(t, firstQueued.result, secondQueued.result)
	observerQueued := awaitAPIWebSocketCompletion(t, observer, true)
	assert.Equal(t, firstQueued.result, observerQueued.result)
	assert.Equal(t, sessionID, firstQueued.sessionID)
	assert.NotEqual(t, uuid.Nil, firstQueued.requestID)

	hold.Release()

	firstCompleted := awaitAPIWebSocketCompletion(t, first, false)
	secondCompleted := awaitAPIWebSocketCompletion(t, second, false)
	assert.Equal(t, firstCompleted.result, secondCompleted.result)
	observerCompleted := awaitAPIWebSocketCompletion(t, observer, false)
	assert.Equal(t, firstCompleted.result, observerCompleted.result)
	assert.Equal(t, sessionID, firstCompleted.sessionID)
	assert.NotEqual(t, uuid.Nil, firstCompleted.requestID)

	// Every client sees the queued message land, under the request ID the
	// sender got back, so any of them can mark it as delivered.
	queuedRequestID := []uuid.UUID{secondQueued.requestID}
	assert.Equal(t, queuedRequestID, firstCompleted.deliveredRequestIDs)
	assert.Equal(t, queuedRequestID, secondCompleted.deliveredRequestIDs)
	assert.Equal(t, queuedRequestID, observerCompleted.deliveredRequestIDs)

	assertNoAPIWebSocketEvent(t, filteredOther)

	messages := collectAllMessages(t, sessionID)
	contents := apiMessageContents(messages)
	assert.Contains(t, contents, apiTestWebSocketInitialMessage)
	assert.Contains(t, contents, apiTestWebSocketActiveMessage)
	assert.Contains(t, contents, apiTestWebSocketQueuedMessage)
	assert.False(t, getSession(t, sessionID).ActiveTurn)
}

// A control surface sends the selected model and reasoning with every message.
// Two such messages sent back to back while the session's worker is still
// starting must be admitted in the order they were sent: the first starts the
// turn, the second joins that turn's queue, and neither is refused.
func TestAPIWebSocketAdmitsMessagesInOrderWhileTheWorkerStarts(t *testing.T) {
	directory := apiTestWebSocketOrderedWorkspacePrefix + uuid.NewString()
	require.NoError(t, integrationInfra.WriteWorkspaceFile(
		t.Context(),
		path.Join(directory, apiTestWebSocketOrderedFixtureFile),
		[]byte(apiTestWebSocketOrderedFixture),
	))

	sessionID := openAPIWorkspaceSessionAt(
		t,
		path.Join(testinfra.ContainerWorkingDirectory, directory),
	)

	connection := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	for _, message := range []string{
		apiTestWebSocketFirstOrderedMessage,
		apiTestWebSocketSecondOrderedMessage,
	} {
		require.NoError(t, writeAPIWebSocketSessionMessage(
			t,
			connection,
			sessionID,
			map[string]string{
				"message":         message,
				"model":           integrationInfra.DefaultModel(),
				"reasoningEffort": apiTestWebSocketReasoningEffort,
			},
		))
	}

	// Reading a completion fails the test on any message.failed, so reaching
	// each one also proves neither message was refused.
	queued := awaitAPIWebSocketCompletion(t, connection, true)
	awaitAPIWebSocketProviderHold(t, hold)
	hold.Release()

	completed := awaitAPIWebSocketCompletion(t, connection, false)
	assert.NotEqual(t, queued.requestID, completed.requestID)
	assert.Equal(
		t,
		[]uuid.UUID{queued.requestID},
		completed.deliveredRequestIDs,
	)

	userMessages := make([]string, 0)
	for _, message := range collectAllMessages(t, sessionID) {
		if message.Role != api.MessageRoleUser {
			continue
		}

		userMessages = append(userMessages, message.Content)
	}

	assert.Equal(
		t,
		[]string{
			apiTestWebSocketFirstOrderedMessage,
			apiTestWebSocketSecondOrderedMessage,
		},
		userMessages,
	)
}

func TestAPIWebSocketRejectsUnauthenticatedAndRoutesToTheOpenedSession(
	t *testing.T,
) {
	t.Run("unauthenticated", func(t *testing.T) {
		dialer := websocket.Dialer{
			HandshakeTimeout: requestTimeout,
		}
		connection, response, err := dialer.Dial(
			apiTestWebSocketURL(t, nil),
			nil,
		)
		require.Error(t, err)
		require.Nil(t, connection)
		require.NotNil(t, response)
		require.Equal(t, http.StatusUnauthorized, response.StatusCode)
		require.NoError(t, response.Body.Close())
	})

	t.Run("opened session", func(t *testing.T) {
		result := sendAPIWebSocketMessage(t, apiTestWebSocketInitialMessage)

		assert.NotEqual(t, uuid.Nil, result.sessionID)
		assert.False(t, result.result.Queued)
		assert.Equal(t, result.sessionID, getSession(t, result.sessionID).Id)
	})
}

func TestAPIWebSocketCarriesTheTurnReasoningEffort(t *testing.T) {
	t.Run("requested level reaches the provider", func(t *testing.T) {
		connection := dialAPIWebSocket(t, nil)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })

		require.NoError(t, writeAPIWebSocketMessageData(t, connection, map[string]string{
			"message":         apiTestWebSocketReasoningMessage,
			"reasoningEffort": apiTestWebSocketReasoningEffort,
		}))
		awaitAPIWebSocketCompletion(t, connection, false)

		assert.Equal(
			t,
			apiTestWebSocketReasoningEffort,
			integrationInfra.LastReasoningEffort(),
		)
	})

	t.Run("no level leaves the provider default", func(t *testing.T) {
		sendAPIWebSocketMessage(t, apiTestWebSocketReasoningMessage)

		assert.Empty(t, integrationInfra.LastReasoningEffort())
	})

	t.Run("unknown level is rejected", func(t *testing.T) {
		connection := dialAPIWebSocket(t, nil)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })

		// Sent through the unvalidated path on purpose: wsapi.MessageSend's
		// reasoningEffort enum would also reject "extreme" locally, which
		// would never let this reach the live server. This subtest proves
		// the running server still answers with its own VALIDATION_FAILED
		// message.failed for the same input.
		require.NoError(t, writeAPIWebSocketSessionMessageUnvalidated(
			connection,
			openAPIWorkspaceSession(t),
			map[string]string{
				"message":         apiTestWebSocketReasoningMessage,
				"reasoningEffort": "extreme",
			},
		))

		failure := awaitAPIWebSocketFailure(t, connection)
		assert.Equal(t, aichteeteapee.ErrorCodeValidationFailed, failure.Code)
	})
}

// A skill named in a message reaches the model as its own message after the
// typed one. The typed message is stored as typed, and the skill is stored as
// an injected message of kind skill.
func TestAPIWebSocketStoresANamedSkillAsAnInjectedMessage(t *testing.T) {
	result := sendAPIWebSocketMessage(t, apiTestWebSocketSkillMessage)

	messages := collectAllMessages(t, result.sessionID)
	require.GreaterOrEqual(t, len(messages), 3)

	typed := messages[len(messages)-3]
	assert.Equal(t, apiTestWebSocketSkillMessage, typed.Content)
	assert.Nil(t, typed.Injected)
	assert.Nil(t, typed.InjectedKind)

	skill := messages[len(messages)-2]
	require.NotNil(t, skill.Injected)
	assert.True(t, *skill.Injected)
	require.NotNil(t, skill.InjectedKind)
	assert.Equal(t, api.MessageInjectedKindSkill, *skill.InjectedKind)
	assert.Equal(t, api.MessageRoleUser, skill.Role)
	assert.Contains(t, skill.Content, "<skill>\n<name>core:planning</name>")
}

func TestAPIWebSocketRefusesAnUnknownSkill(t *testing.T) {
	connection := dialAPIWebSocket(t, nil)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	require.NoError(t, writeAPIWebSocketMessage(
		t,
		connection,
		apiTestWebSocketUnknownSkillMessage,
	))

	failure := awaitAPIWebSocketFailure(t, connection)
	assert.Equal(t, aichteeteapee.ErrorCodeValidationFailed, failure.Code)
	assert.Contains(
		t,
		failure.Message,
		`unknown skill "no-such-skill" in :no-such-skill; available skills: `,
	)
	assert.Contains(t, failure.Message, "core:planning")
	assert.NotContains(t, failure.Message, ".go:")
}

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

// awaitAPIWebSocketEventOfType reads frames off connection until one of type
// eventType arrives, failing the test on a message.failed in between.
func awaitAPIWebSocketEventOfType(
	t *testing.T,
	connection *websocket.Conn,
	eventType string,
) dabluveees.Event {
	t.Helper()

	for {
		event := readWebSocketEvent(t, connection)
		if string(event.Type) == apiTestWebSocketMessageFailed {
			t.Fatalf("WebSocket message failed: %s", event.Data)
		}

		if string(event.Type) == eventType {
			return event
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

func createAPIWebSocketSession(t *testing.T, message string) uuid.UUID {
	t.Helper()
	result := sendAPIWebSocketMessage(t, message)
	assert.False(t, result.result.Queued)

	return result.sessionID
}

func sendAPIWebSocketMessage(
	t *testing.T,
	message string,
) apiTestWebSocketObservation {
	t.Helper()

	// Filter to the session the message goes to. A global socket also gets
	// other sessions' frames, so a completion from an earlier test's turn
	// that ends just as this one starts would be mistaken for this reply.
	sessionID := openAPIWorkspaceSession(t)
	connection := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, writeAPIWebSocketMessage(t, connection, message))

	result := awaitAPIWebSocketCompletion(t, connection, false)
	assert.NotEqual(t, uuid.Nil, result.sessionID)

	return result
}

func writeAPIWebSocketMessage(
	t *testing.T,
	connection *websocket.Conn,
	message string,
) error {
	t.Helper()

	return writeAPIWebSocketMessageData(
		t,
		connection,
		map[string]string{"message": message},
	)
}

func writeAPIWebSocketMessageData(
	t *testing.T,
	connection *websocket.Conn,
	data map[string]string,
) error {
	t.Helper()

	return writeAPIWebSocketSessionMessage(
		t,
		connection,
		openAPIWorkspaceSession(t),
		data,
	)
}

// writeAPIWebSocketSessionMessage builds and sends one message.send, failing
// the test immediately when the built frame does not decode through the
// generated wsapi.MessageSend contract. This is the one place every
// message.send a test sends goes through, so a stale field name or a shape
// the spec no longer accepts is caught here rather than surfacing as an
// unrelated failure further down a test.
func writeAPIWebSocketSessionMessage(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	data map[string]string,
) error {
	t.Helper()

	event := dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		data,
	).SetMetadata(
		apiTestWebSocketSessionIDParameter,
		sessionID.String(),
	)

	assertValidMessageSend(t, event)

	return connection.WriteJSON(event)
}

// writeAPIWebSocketSessionMessageUnvalidated sends message.send without the
// wsapi.MessageSend contract check writeAPIWebSocketSessionMessage normally
// runs. Its only caller deliberately sends a reasoningEffort the contract
// itself now also closes off, to prove the live server still rejects it;
// validating that frame before it is written would catch the bad value
// locally and the test would never reach the server at all.
func writeAPIWebSocketSessionMessageUnvalidated(
	connection *websocket.Conn,
	sessionID uuid.UUID,
	data map[string]string,
) error {
	event := dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		data,
	).SetMetadata(
		apiTestWebSocketSessionIDParameter,
		sessionID.String(),
	)

	return connection.WriteJSON(event)
}

// assertValidMessageSend fails the test immediately, naming the frame type
// and the validation error, when a message.send a test is about to write
// does not decode through the generated wsapi contract.
func assertValidMessageSend(t *testing.T, event dabluveees.Event) {
	t.Helper()

	encoded, err := json.Marshal(event)
	require.NoError(t, err)

	target := wsapi.MessageSend{}
	require.NoErrorf(
		t,
		target.UnmarshalJSON(encoded),
		"%s failed the wsapi.MessageSend contract: %s",
		apiTestWebSocketMessageSend,
		encoded,
	)
}

// openAPIWorkspaceSession opens the container's workspace through the real
// control endpoint. The service starts with no sessions, so every turn these
// tests run begins here. Opening the same workspace again resumes it, so
// calling this per message keeps returning the one session.
func openAPIWorkspaceSession(t *testing.T) uuid.UUID {
	t.Helper()

	return openAPIWorkspaceSessionAt(t, testinfra.ContainerWorkingDirectory)
}

// openAPIWorkspaceSessionAt opens one workspace below the container's root.
// A workspace no test has opened yet gets a session with no worker, which is
// how a test sends messages while that worker is still starting.
func openAPIWorkspaceSessionAt(t *testing.T, workspace string) uuid.UUID {
	t.Helper()

	body, err := json.Marshal(map[string]string{
		"workspace": workspace,
	})
	require.NoError(t, err)

	response := apiRequest(
		t,
		http.MethodPost,
		apiBasePath+"/sessions/open",
		body,
		map[string]string{
			headerContentType:   jsonMediaType,
			headerAuthorization: bearerPrefix + testinfra.TestAPIToken,
		},
	)
	require.Equal(t, http.StatusOK, response.StatusCode)

	opened := decodeResponse[apiTestOpenedSession](t, response)
	require.Equal(t, workspace, opened.Session.Workspace)

	return opened.Session.ID
}

type apiTestOpenedSession struct {
	Created bool `json:"created"`
	Session struct {
		ID        uuid.UUID `json:"id"`
		Workspace string    `json:"workspace"`
	} `json:"session"`
}

func dialAPIWebSocket(
	t *testing.T,
	filterSessionID *uuid.UUID,
) *websocket.Conn {
	t.Helper()

	dialer := apiTestWebSocketDialer()
	connection, response, err := dialer.Dial(
		apiTestWebSocketURL(t, filterSessionID),
		nil,
	)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}
	require.NoError(t, err)
	require.Equal(t, apiTestWebSocketSubprotocol, connection.Subprotocol())

	return connection
}

func apiTestWebSocketDialer() websocket.Dialer {
	return websocket.Dialer{
		HandshakeTimeout: requestTimeout,
		Subprotocols: []string{
			apiTestWebSocketSubprotocol,
			apiTestWebSocketBearerPrefix + base64.RawURLEncoding.EncodeToString(
				[]byte(testinfra.TestAPIToken),
			),
		},
	}
}

func apiTestWebSocketURL(
	t *testing.T,
	filterSessionID *uuid.UUID,
) string {
	t.Helper()

	endpoint, err := url.Parse(integrationInfra.APIURL(apiTestWebSocketPath))
	require.NoError(t, err)
	endpoint.Scheme = "ws"
	query := endpoint.Query()
	if filterSessionID != nil {
		query.Set(
			apiTestWebSocketSessionIDParameter,
			filterSessionID.String(),
		)
	}
	endpoint.RawQuery = query.Encode()

	return endpoint.String()
}

func awaitAPIWebSocketProviderHold(
	t *testing.T,
	hold *testinfra.CompletionHold,
) {
	t.Helper()

	select {
	case <-hold.Observed:
	case <-time.After(apiTestWebSocketReadTimeout):
		t.Fatal("WebSocket message did not reach the provider")
	}
}

func awaitAPIWebSocketCompletion(
	t *testing.T,
	connection *websocket.Conn,
	wantQueued bool,
) apiTestWebSocketObservation {
	t.Helper()

	return awaitAPIWebSocketCompletionForEvent(t, connection, wantQueued,
		uuid.Nil)
}

func awaitAPIWebSocketCompletionForEvent(
	t *testing.T,
	connection *websocket.Conn,
	wantQueued bool,
	triggeringEventID uuid.UUID,
) apiTestWebSocketObservation {
	t.Helper()

	deadline := time.Now().Add(apiTestWebSocketReadTimeout)
	agentEventTypes := make([]string, 0)
	deliveredRequestIDs := make([]uuid.UUID, 0)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))

		received := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&received))
		assertValidServerFrame(t, received)

		if triggeringEventID != uuid.Nil && (received.TriggeredBy == nil ||
			*received.TriggeredBy != triggeringEventID) {
			continue
		}

		switch string(received.Type) {
		case apiTestWebSocketMessageCompleted:
			result := apiTestWebSocketResult{}
			require.NoError(t, json.Unmarshal(received.Data, &result))
			if result.Queued == wantQueued {
				sessionID, requestID := apiTestWebSocketMetadata(t, received)

				return apiTestWebSocketObservation{
					result:              result,
					sessionID:           sessionID,
					requestID:           requestID,
					agentEventTypes:     agentEventTypes,
					deliveredRequestIDs: deliveredRequestIDs,
				}
			}
		case apiTestWebSocketMessageFailed:
			t.Fatalf("WebSocket message failed: %s", received.Data)
		default:
			_, requestID := apiTestWebSocketMetadata(t, received)
			agentEventTypes = append(agentEventTypes, string(received.Type))
			if received.Type == apiTestWebSocketMessageDelivered {
				deliveredRequestIDs = append(deliveredRequestIDs, requestID)
			}
		}
	}
}

func awaitAPIWebSocketFailure(
	t *testing.T,
	connection *websocket.Conn,
) apiTestWebSocketFailure {
	t.Helper()

	deadline := time.Now().Add(apiTestWebSocketReadTimeout)
	for {
		require.NoError(t, connection.SetReadDeadline(deadline))

		received := dabluveees.Event{}
		require.NoError(t, connection.ReadJSON(&received))
		assertValidServerFrame(t, received)

		switch string(received.Type) {
		case apiTestWebSocketMessageFailed:
			failure := apiTestWebSocketFailure{}
			require.NoError(t, json.Unmarshal(received.Data, &failure))

			return failure
		case apiTestWebSocketMessageCompleted:
			t.Fatal("WebSocket message completed instead of failing")
		}
	}
}

// apiTestWebSocketMetadata reads metadata.sessionId, required on every
// session frame, and metadata.requestId, which an agent.run.* or
// notice.created frame carries no value for because it belongs to no one
// turn's request. A frame missing it returns uuid.Nil for the request ID.
func apiTestWebSocketMetadata(
	t *testing.T,
	event dabluveees.Event,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	require.NotNil(t, event.Metadata)
	sessionValue, found := event.Metadata.Get(apiTestWebSocketMetadataSessionID)
	require.True(t, found)
	sessionText, ok := sessionValue.(string)
	require.True(t, ok)
	sessionID, err := uuid.Parse(sessionText)
	require.NoError(t, err)

	requestID := uuid.Nil
	if requestValue, found := event.Metadata.Get(
		apiTestWebSocketMetadataRequestID,
	); found {
		requestText, ok := requestValue.(string)
		require.True(t, ok)
		requestID, err = uuid.Parse(requestText)
		require.NoError(t, err)
	}

	return sessionID, requestID
}

func assertNoAPIWebSocketEvent(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	require.NoError(
		t,
		connection.SetReadDeadline(
			time.Now().Add(apiTestWebSocketIsolationTimeout),
		),
	)

	received := dabluveees.Event{}
	err := connection.ReadJSON(&received)
	require.Error(t, err)

	var networkErr net.Error

	isNetworkErr := errors.As(err, &networkErr)
	require.True(t, isNetworkErr)
	assert.True(t, networkErr.Timeout())
}
