package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	wakeTestHandlerType = "app.error"
	wakeTestInstruction = "Read the error report and find the failing code."
	wakeTestReply       = "looked at it"
	wakeTestPoll        = 10 * time.Millisecond
	wakeTestTimeout     = 5 * time.Second

	wakeTestHandlerDocument = `---
type: app.error
delivery: wake
---
Read the error report and find the failing code.`

	// wakeTestQueueHandlerDocument declares the same type with no delivery of
	// its own, so the notice's own mode decides.
	wakeTestQueueHandlerDocument = `---
type: app.error
---
Read the error report and find the failing code.`

	// wakeTestHeldHandlerDocument holds the type back even when the producer
	// asked for a wake.
	wakeTestHeldHandlerDocument = `---
type: app.error
delivery: queue
---
Read the error report and find the failing code.`
)

func writeWakeHandler(t *testing.T, configDirectory string, document string) {
	t.Helper()

	writeRuntimeFile(
		t,
		filepath.Join(
			configDirectory,
			".agents",
			"events",
			wakeTestHandlerType+".md",
		),
		document,
	)
}

func wakeTestNotice(sessionID uuid.UUID) events.Notice {
	return events.Notice{
		SessionID: sessionID,
		Type:      wakeTestHandlerType,
		Source:    "webhook",
		Summary:   "checkout returned 500",
		Delivery:  events.DeliveryWake,
	}
}

func openWakeSession(t *testing.T, fixture runtimeFixture) uuid.UUID {
	t.Helper()

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "open the session",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	return result.SessionID
}

// A declared handler plus an idle session is the whole point: an outside
// report starts a turn with nobody sending a message.
func TestPublishEventWakesAnIdleSessionWithAHandler(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text(wakeTestReply),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	published, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)
	assert.Equal(t, events.DeliveryWake, published.Delivery)

	require.Eventually(t, func() bool {
		messages, listErr := fixture.store.ListMessages(
			context.Background(),
			sessionID,
			session.ListMessagesOptions{Order: session.PageOrderAscending},
		)

		return listErr == nil && containsContent(messages, wakeTestReply)
	}, wakeTestTimeout, wakeTestPoll, "the woken turn ran and answered")

	messages, err := fixture.store.ListMessages(
		context.Background(),
		sessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	assert.True(
		t,
		containsContent(messages, wakeTestInstruction),
		"the woken turn ran the handler instruction as its task",
	)
	assert.True(
		t,
		containsContent(messages, sessionEventsOpenTag),
		"the event reached the turn as quoted data",
	)
}

// Pointing a webhook at Peen must not start spending money before anyone has
// said what the agent should do about that event type.
func TestPublishEventWithNoHandlerStartsNothing(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))

	sessionID := openWakeSession(t, fixture)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	assert.Never(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 0
	}, time.Second, wakeTestPoll, "the event stays queued, no turn consumed it")
}

// A handler that declares no delivery of its own leaves the producer's mode in
// force, so a queued event still starts nothing.
func TestPublishEventQueueDeliveryNeverWakes(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestQueueHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	notice := wakeTestNotice(sessionID)
	notice.Delivery = events.DeliveryQueue

	_, err := fixture.runtime.PublishEvent(context.Background(), notice)
	require.NoError(t, err)

	assert.Never(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 0
	}, time.Second, wakeTestPoll, "queue delivery starts no turn")
}

// The deployment owns what a type means. A webhook posting error logs will not
// set delivery itself, and the API defaults the field to queue, so a handler
// declaring wake has to promote it or the setting can never fire.
func TestPublishEventHandlerDeliveryPromotesAQueuedEvent(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text(wakeTestReply),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	notice := wakeTestNotice(sessionID)
	notice.Delivery = events.DeliveryQueue

	_, err := fixture.runtime.PublishEvent(context.Background(), notice)
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 0
	}, wakeTestTimeout, wakeTestPoll, "the handler must promote it to a wake")
}

// A turn nobody asked for has to say why it exists. With several events queued
// at once, the incidental drain cannot tell a reader which one caused the wake,
// so the originating event is recorded on the turn itself.
func TestPublishEventRecordsTheOriginatingEventOnTheWokenTurn(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text(wakeTestReply),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	published, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	var payload turnStartedPayload

	assert.Eventually(t, func() bool {
		payload = wokenTurnStartedPayload(t, fixture, sessionID)

		return payload.OriginEventID != ""
	}, wakeTestTimeout, wakeTestPoll, "the wake must record its event")

	assert.Equal(t, published.ID.String(), payload.OriginEventID)
	assert.Equal(t, wakeTestHandlerType, payload.OriginEventType)
}

// wokenTurnStartedPayload returns the newest turn.started payload that carries
// an origin, which is the wake-started turn rather than the one that opened
// the session.
func wokenTurnStartedPayload(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
) turnStartedPayload {
	t.Helper()

	query := repositories.Use(fixture.handle.GormDB)

	rows, err := query.Event.WithContext(context.Background()).
		Where(
			query.Event.SessionID.Eq(sessionID),
			query.Event.EventType.Eq(EventTypeTurnStarted),
		).
		Find()
	require.NoError(t, err)

	for _, row := range rows {
		payload := turnStartedPayload{}
		require.NoError(t, json.Unmarshal([]byte(row.PayloadJSON), &payload))

		if payload.OriginEventID != "" {
			return payload
		}
	}

	return turnStartedPayload{}
}

// The override runs both ways: a handler declaring queue holds the type back
// even when the producer asked for a wake.
func TestPublishEventHandlerDeliveryHoldsBackARequestedWake(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHeldHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	assert.Never(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 0
	}, time.Second, wakeTestPoll, "a handler declaring queue starts no turn")
}

// A session runs one turn at a time, so a wake arriving mid-turn must not try
// to start a second one. It degrades to a queued delivery that the running
// turn picks up at its next tool boundary.
func TestPublishEventOnABusySessionDegradesToQueued(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	// Hold a turn so the session reports as active, exactly as it would while
	// a real turn is running.
	lease, err := fixture.store.AcquireTurn(
		context.Background(),
		sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: fixture.workspace,
			Messages: []session.MessageInput{{
				Role:    models.MessageRoleUser,
				Content: "holding the session",
			}},
		},
	)
	require.NoError(t, err)

	defer fixture.store.ReleaseTurn(lease)

	require.True(t, fixture.store.IsActive(sessionID))

	_, err = fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err, "a busy session is not an error for the publisher")

	assert.Never(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 0
	}, time.Second, wakeTestPoll, "no second turn started, the event waits")
}

func TestPublishEventRejectsUnknownSession(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(uuid.New()),
	)
	require.Error(t, err)
}

func TestPublishEventWithoutABusFails(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)

	fixture.runtime.eventBus = nil

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.ErrorIs(t, err, ErrEventsUnavailable)
}

func TestWakeLimiterBoundsWakesPerWindow(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	other := uuid.New()
	now := time.Now()

	limiter := newWakeLimiter(2)

	assert.True(t, limiter.allow(sessionID, now))
	assert.True(t, limiter.allow(sessionID, now))
	assert.False(t, limiter.allow(sessionID, now), "the third is over the bound")
	assert.True(t, limiter.allow(other, now), "the bound is per session")

	assert.True(
		t,
		limiter.allow(sessionID, now.Add(wakeWindow+time.Minute)),
		"the window slides",
	)
}

func TestWakeLimiterZeroDisablesTheBound(t *testing.T) {
	t.Parallel()

	limiter := newWakeLimiter(0)
	sessionID := uuid.New()
	now := time.Now()

	for range 100 {
		require.True(t, limiter.allow(sessionID, now))
	}
}

func TestWakeMessageNamesTheHandlerAgent(t *testing.T) {
	t.Parallel()

	withoutAgent := wakeMessage(newWakeHandler(""))
	assert.Equal(t, wakeTestInstruction, withoutAgent)

	withAgent := wakeMessage(newWakeHandler("incident-responder"))
	assert.Contains(t, withAgent, wakeTestInstruction)
	assert.Contains(t, withAgent, "incident-responder")
}

func newWakeHandler(agent string) harness.EventHandler {
	return harness.EventHandler{
		Type:         wakeTestHandlerType,
		Agent:        agent,
		Delivery:     events.DeliveryWake,
		Instructions: wakeTestInstruction,
	}
}

func containsContent(page *session.MessagePage, needle string) bool {
	for _, message := range page.Items {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}

	return false
}
