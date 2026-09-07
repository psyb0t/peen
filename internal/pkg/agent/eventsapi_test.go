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

func apiEventRequest(eventType string) api.SessionEventRequest {
	return api.SessionEventRequest{
		Type:    eventType,
		Summary: "checkout returned 500",
	}
}

// An outside caller must not be able to publish a job or agent event, because
// those carry Peen's own guarantee that the thing actually happened.
func TestPublishSessionEventRejectsReservedTypes(t *testing.T) {
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
			published, err := fixture.runtime.PublishSessionEvent(
				context.Background(),
				sessionID,
				apiEventRequest(tc.eventType),
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

func TestPublishSessionEventRoundTripsData(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)

	data := map[string]any{"status": float64(500), "path": "/checkout"}
	request := apiEventRequest("app.error")
	request.Data = &data

	published, err := fixture.runtime.PublishSessionEvent(
		context.Background(),
		sessionID,
		request,
	)
	require.NoError(t, err)
	require.NotNil(t, published.Data)
	assert.Equal(t, data, *published.Data)
}

func TestPublishSessionEventRejectsUnknownDelivery(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)

	delivery := api.SessionEventRequestDelivery("explode")
	request := apiEventRequest("app.error")
	request.Delivery = &delivery

	_, err := fixture.runtime.PublishSessionEvent(
		context.Background(),
		sessionID,
		request,
	)
	require.ErrorIs(t, err, events.ErrInvalidDelivery)
}

// Listing must not consume. An operator reading the queue over HTTP would
// otherwise steal events the agent has not been told about.
func TestListSessionEventsPeeksWithoutConsuming(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)

	_, err := fixture.runtime.PublishSessionEvent(
		context.Background(),
		sessionID,
		apiEventRequest("app.error"),
	)
	require.NoError(t, err)

	for range 3 {
		page, listErr := fixture.runtime.ListSessionEvents(
			context.Background(),
			sessionID,
		)
		require.NoError(t, listErr)
		require.Len(t, page.Events, 1, "listing repeatedly returns the same one")
		assert.Equal(t, 0, int(page.Dropped))
	}

	assert.Equal(t, 1, fixture.eventBus.Pending(sessionID))
}

func TestListSessionEventsRejectsUnknownSession(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())

	_, err := fixture.runtime.ListSessionEvents(
		context.Background(),
		uuid.New(),
	)
	require.Error(t, err)
}
