package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const feedPath = apiBaseURL + "/session/feed"

func TestServerListSessionFeedDefaultsAndErrors(t *testing.T) {
	sessionID := uuid.New()

	testCases := []struct {
		name       string
		query      string
		runtime    *testRuntime
		wantStatus int
		wantCode   aichteeteapee.ErrorCode
	}{
		{
			name:       "defaults succeed",
			runtime:    &testRuntime{sessionID: sessionID},
			wantStatus: http.StatusOK,
		},
		{
			name:  "negative cursor is rejected",
			query: "?cursor=-1",
			runtime: &testRuntime{
				sessionID: sessionID,
				feedErr:   commerr.ErrValidationFailed,
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:  "zero limit is rejected",
			query: "?limit=0",
			runtime: &testRuntime{
				sessionID: sessionID,
				feedErr:   commerr.ErrValidationFailed,
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name:  "a limit over the maximum is rejected",
			query: "?limit=201",
			runtime: &testRuntime{
				sessionID: sessionID,
				feedErr:   commerr.ErrValidationFailed,
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   aichteeteapee.ErrorCodeValidationFailed,
		},
		{
			name: "an unknown session is reported",
			runtime: &testRuntime{
				sessionID: sessionID,
				feedErr:   commerr.ErrNotFound,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   ErrorCodeSessionNotFound,
		},
		{
			name: "a cursor at the head returns an empty page",
			runtime: &testRuntime{
				sessionID: sessionID,
				feedPage: &session.FeedPage{
					NextCursor: 5,
					HasMore:    false,
					Head:       5,
				},
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			instance, err := newTestServer(Dependencies{Runtime: tc.runtime})
			require.NoError(t, err)

			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				feedPath+tc.query,
				nil,
			)
			request.Header.Set(headerSessionID, sessionID.String())

			recorder := httptest.NewRecorder()
			instance.inProcessHandler.ServeHTTP(recorder, request)

			assert.Equal(t, tc.wantStatus, recorder.Code)

			if tc.wantCode != "" {
				assertErrorCode(t, recorder, tc.wantCode)
			}
		})
	}
}

func TestServerListSessionFeedRequiresTheSessionHeader(t *testing.T) {
	instance, err := newTestServer(Dependencies{Runtime: &testRuntime{}})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		feedPath,
		nil,
	)

	recorder := httptest.NewRecorder()
	instance.inProcessHandler.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
}

// Each rendered item matches, byte for byte, what feedItemFrame would produce
// for a live frame of the same kind, because ListSessionFeed and
// BroadcastFeedItems both call it.
func TestServerListSessionFeedRendersEveryKindLikeTheLiveFrame(t *testing.T) {
	sessionID := uuid.New()
	requestID := uuid.New()
	agentRunID := uuid.New()
	parentTurnID := uuid.New()
	createdAt := time.Now().UTC().Truncate(time.Second)

	notice := &models.SessionNotice{
		ID:        uuid.New(),
		SessionID: sessionID,
		Type:      "app.ready",
		Source:    "example",
		Summary:   "ready",
		DataJSON:  `{"ok":true}`,
		Delivery:  models.NoticeDeliveryQueue,
		CreatedAt: createdAt,
	}

	page := &session.FeedPage{
		Items: []session.FeedItem{
			{
				SessionID:   sessionID,
				Sequence:    1,
				ID:          uuid.New(),
				Kind:        session.FeedKindEvent,
				EventType:   "turn.completed",
				RequestID:   requestID,
				PayloadJSON: `{"model":"m","text":"hi"}`,
				CreatedAt:   createdAt,
			},
			{
				SessionID:    sessionID,
				Sequence:     2,
				ID:           uuid.New(),
				Kind:         session.FeedKindAgentRunEvent,
				EventType:    "agent.run.started",
				AgentRunID:   agentRunID,
				ParentTurnID: parentTurnID,
				PayloadJSON:  `{"task":"do it"}`,
				CreatedAt:    createdAt,
			},
			{
				SessionID: sessionID,
				Sequence:  3,
				ID:        notice.ID,
				Kind:      session.FeedKindNotice,
				EventType: session.FeedEventTypeNoticeCreated,
				Notice:    notice,
				CreatedAt: createdAt,
			},
		},
		NextCursor: 3,
		HasMore:    false,
		Head:       3,
	}

	instance, err := newTestServer(Dependencies{
		Runtime: &testRuntime{sessionID: sessionID, feedPage: page, feedLimit: 100},
	})
	require.NoError(t, err)

	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		feedPath,
		nil,
	)
	request.Header.Set(headerSessionID, sessionID.String())

	recorder := httptest.NewRecorder()
	instance.inProcessHandler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)

	body := decodeFeedPage(t, recorder)
	require.Len(t, body.Items, 3)
	assert.Equal(t, int32(100), body.Limit)
	assert.EqualValues(t, 3, body.NextCursor)
	assert.False(t, body.HasMore)
	assert.EqualValues(t, 3, body.Head)

	for index, item := range page.Items {
		rendered, renderErr := feedItemFrame(item)
		require.NoError(t, renderErr)

		frame := body.Items[index]
		assert.Equal(t, item.Sequence, frame.Sequence)
		assert.Equal(t, item.ID, frame.Id)
		assert.Equal(t, string(rendered.Type), frame.Type)
		assert.Equal(t, sessionID, frame.SessionId)

		var decodedRendered any
		require.NoError(t, json.Unmarshal(rendered.Data, &decodedRendered))
		assert.Equal(t, decodedRendered, frame.Data)
	}

	// Only the event-kind item carries a request ID.
	require.NotNil(t, body.Items[0].RequestId)
	assert.Equal(t, requestID, *body.Items[0].RequestId)
	assert.Nil(t, body.Items[1].RequestId)
	assert.Nil(t, body.Items[2].RequestId)
}

// A replayed frame and the live frame for the same row must carry the same
// timestamp, and that timestamp is the row's createdAt, not render time.
func TestFeedItemFrameTimestampMatchesCreatedAt(t *testing.T) {
	createdAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	item := session.FeedItem{
		SessionID:   uuid.New(),
		Sequence:    1,
		ID:          uuid.New(),
		Kind:        session.FeedKindEvent,
		EventType:   "turn.completed",
		PayloadJSON: `{"model":"m","text":"hi"}`,
		CreatedAt:   createdAt,
	}

	// Rendering twice stands in for the live path and the replay path: both
	// call feedItemFrame, so an hour-old createdAt that still comes back
	// unchanged on each call proves the timestamp is the row's, not
	// time.Now() at render.
	live, err := feedItemFrame(item)
	require.NoError(t, err)

	replayed, err := feedItemFrame(item)
	require.NoError(t, err)

	assert.Equal(t, createdAt.Unix(), live.Timestamp)
	assert.Equal(t, createdAt.Unix(), replayed.Timestamp)
}

func decodeFeedPage(t *testing.T, recorder *httptest.ResponseRecorder) api.FeedPage {
	t.Helper()

	page := api.FeedPage{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))

	return page
}
