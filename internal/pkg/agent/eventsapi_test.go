package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/events"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func apiNoticeRequest(eventType string) api.SessionNoticeRequest {
	return api.SessionNoticeRequest{
		Type:    eventType,
		Summary: "checkout returned 500",
	}
}

// An outside caller must not be able to publish a job or agent event, because
// those carry Peen's own guarantee that the thing actually happened.
func TestPublishSessionNoticeRejectsReservedTypes(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)

	testCases := []struct {
		name      string
		eventType string
		wantErr   error
	}{
		{name: "deployment type", eventType: "app.error"},
		{
			name:      "forged job event",
			eventType: events.TypeJobExited,
			wantErr:   events.ErrReservedType,
		},
		{
			name:      "forged agent event",
			eventType: events.TypeAgentFinished,
			wantErr:   events.ErrReservedType,
		},
		{
			name:      "invented job type",
			eventType: "job.whatever",
			wantErr:   events.ErrReservedType,
		},
		{
			name:      "malformed type",
			eventType: "App.Error",
			wantErr:   events.ErrInvalidType,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			published, err := fixture.runtime.PublishSessionNotice(
				context.Background(),
				sessionID,
				apiNoticeRequest(tc.eventType),
			)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, published)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, published)
			assert.Equal(t, tc.eventType, published.Type)
			assert.Equal(t, externalEventSource, published.Source)
		})
	}
}

func TestPublishSessionNoticeRoundTripsData(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)

	data := map[string]any{"status": float64(500), "path": "/checkout"}
	request := apiNoticeRequest("app.error")
	request.Data = &data

	published, err := fixture.runtime.PublishSessionNotice(
		context.Background(),
		sessionID,
		request,
	)
	require.NoError(t, err)
	require.NotNil(t, published.Data)
	assert.Equal(t, data, *published.Data)
}

func TestPublishSessionNoticeRejectsUnknownDelivery(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)

	delivery := api.SessionNoticeRequestDelivery("explode")
	request := apiNoticeRequest("app.error")
	request.Delivery = &delivery

	_, err := fixture.runtime.PublishSessionNotice(
		context.Background(),
		sessionID,
		request,
	)
	require.ErrorIs(t, err, events.ErrInvalidDelivery)
}

// Listing must not consume. An operator reading notice history must never
// steal an event the agent has not been told about.
func TestListSessionNoticesDoesNotConsume(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)

	_, err := fixture.runtime.PublishSessionNotice(
		context.Background(),
		sessionID,
		apiNoticeRequest("app.error"),
	)
	require.NoError(t, err)

	for range 3 {
		page, listErr := fixture.runtime.ListSessionNotices(
			context.Background(),
			api.ListSessionNoticesParams{XSessionID: sessionID},
		)
		require.NoError(t, listErr)
		require.Len(t, page.Notices, 1, "listing repeatedly returns the same one")
	}

	assert.Equal(t, 1, fixture.eventBus.Pending(sessionID))
}

func TestListSessionNoticesRejectsUnknownSession(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())

	_, err := fixture.runtime.ListSessionNotices(
		context.Background(),
		api.ListSessionNoticesParams{XSessionID: uuid.New()},
	)
	require.Error(t, err)
}
