package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
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

	wakeTestWrappedInstruction = "<handler>\n<event>app.error</event>\n\n" +
		wakeTestInstruction + "\n</handler>"

	wakeTestPoll    = 10 * time.Millisecond
	wakeTestTimeout = 5 * time.Second

	wakeTestJobEventType       = "job.exited"
	wakeTestJobHandlerDocument = "---\ntype: job.exited\ndelivery: wake\n---\nA background job ended. Say why it stopped."
	wakeTestCrashCommand       = "sleep 0.3; echo fatal >&2; exit 1"
	wakeTestCrashPurpose       = "start the app in the background"
	wakeTestCrashCallID        = "call-crash"

	wakeTestHandlerDocument = `---
type: app.error
delivery: wake
---
Read the error report and find the failing code.`

	// wakeTestSkillHandlerDocument names a skill in its body.
	wakeTestSkillHandlerDocument = `---
type: app.error
delivery: wake
---
Follow :review-rules for this error.`

	// wakeTestUnknownSkillHandlerDocument names a skill that does not exist.
	wakeTestUnknownSkillHandlerDocument = `---
type: app.error
delivery: wake
---
Retry with :missing-skill later.`

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

	// Nobody typed the handler's instruction, so it is stored as injected,
	// in its tag, while the session's own opening prompt stays a person's
	// message.
	storedWake := false

	for _, message := range messages.Items {
		if message.Role != models.MessageRoleUser {
			continue
		}

		if message.Content == wakeTestWrappedInstruction {
			storedWake = true

			assert.True(t, message.Injected, "the wake message is injected")
		}

		isHandlerInstruction := strings.Contains(
			message.Content,
			wakeTestInstruction,
		)
		isEventReport := strings.Contains(message.Content, sessionEventsOpenTag)
		assert.Equal(
			t,
			isHandlerInstruction || isEventReport,
			message.Injected,
			"user message %q",
			message.Content,
		)
	}

	assert.True(t, storedWake, "the tagged wake message is stored")
}

// The model receives the handler block, then the events block. The stored rows
// must come in that order too, or a later turn replays them the other way
// round.
func TestPublishEventWakeStoresMessagesInTheOrderTheModelReceivesThem(
	t *testing.T,
) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text(wakeTestReply),
	)
	fixture := newRuntimeFixture(t, driver)
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		messages, listErr := fixture.store.ListMessages(
			context.Background(),
			sessionID,
			session.ListMessagesOptions{Order: session.PageOrderAscending},
		)

		return listErr == nil && containsContent(messages, wakeTestReply)
	}, wakeTestTimeout, wakeTestPoll, "the woken turn ran and answered")

	requests := driver.Requests()
	require.Len(t, requests, 2)

	// The request holds the system prompt, then the stored history, then the
	// woken turn's own messages in the order the model gets them.
	received := messageTexts(requests[1].Messages[1:])
	require.Len(t, received, 4)

	stored := make([]string, 0, len(received))

	for _, row := range orderedMessages(t, fixture, sessionID) {
		stored = append(stored, row.Content)
	}

	require.Len(t, stored, 5)
	assert.Equal(t, received, stored[:4])
	assert.Equal(t, wakeTestWrappedInstruction, stored[2])
	assert.Contains(t, stored[3], sessionEventsOpenTag)
}

// The turn-start rows are stored in the order the prompt hands them to the
// model: the message, the skills it names, then the events that waited.
func TestTurnStartMessagesFollowTheOrderOfThePrompt(t *testing.T) {
	t.Parallel()

	skill := skillMessage{name: "alpha", text: "<skill>alpha</skill>"}

	rows := turnStartMessages(
		"<events>waiting</events>",
		"",
		"the message",
		false,
		[]skillMessage{skill},
	)

	contents := make([]string, 0, len(rows))
	for _, row := range rows {
		contents = append(contents, row.Content)
	}

	assert.Equal(
		t,
		[]string{
			"the message",
			"<skill>alpha</skill>",
			"<events>waiting</events>",
		},
		contents,
	)
	assert.False(t, rows[0].Injected)
	assert.True(t, rows[1].Injected)
	assert.True(t, rows[2].Injected)
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

// A handler that declares no delivery of its own leaves the producer's mode in
// force, so an event that asks for a wake starts a turn.
func TestPublishEventWakeDeliveryWakesThroughAHandlerWithoutDelivery(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text(wakeTestReply),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestQueueHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 0
	}, wakeTestTimeout, wakeTestPoll, "the event's own wake must start a turn")
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

// A controller runs no model loop of its own. With a wake runner set, the
// woken turn goes to that runner, which sends it to the session's worker, and
// the runtime that published the event runs nothing itself.
func TestPublishEventHandsTheWakeTurnToTheWakeRunner(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	type wakeCall struct {
		sessionID uuid.UUID
		request   MessageRequest
	}

	calls := make(chan wakeCall, 1)
	fixture.runtime.SetWakeRunner(func(
		_ context.Context,
		woken uuid.UUID,
		request MessageRequest,
	) error {
		calls <- wakeCall{sessionID: woken, request: request}

		return nil
	})

	published, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	var call wakeCall

	select {
	case call = <-calls:
	case <-time.After(wakeTestTimeout):
		t.Fatal("the wake never reached the wake runner")
	}

	assert.Equal(t, sessionID, call.sessionID)
	require.NotNil(t, call.request.SessionID)
	assert.Equal(t, sessionID, *call.request.SessionID)
	assert.Contains(t, call.request.Message, wakeTestInstruction)
	require.NotNil(t, call.request.Origin)
	assert.Equal(t, published.ID, call.request.Origin.EventID)
	assert.Equal(t, wakeTestHandlerType, call.request.Origin.EventType)

	assert.Empty(
		t,
		wokenTurnStartedPayload(t, fixture, sessionID).OriginEventID,
		"the publishing runtime must not run the woken turn itself",
	)
}

// A message that carries an origin runs as a woken turn: the turn records the
// event it came from and stores its prompt as injected.
func TestRunMessageRecordsTheTurnOrigin(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text(wakeTestReply),
	))

	sessionID := openWakeSession(t, fixture)
	notice, err := fixture.runtime.persistAndPublishNotice(t.Context(),
		events.Notice{
			SessionID: sessionID, Type: wakeTestHandlerType,
			Source: "example", Summary: "ready", Delivery: events.DeliveryQueue,
		})
	require.NoError(t, err)
	origin := TurnOrigin{EventID: notice.ID, EventType: wakeTestHandlerType}

	_, err = fixture.runtime.RunMessage(
		context.Background(),
		MessageRequest{
			SessionID: &sessionID,
			Message:   wakeTestInstruction,
			Origin:    &origin,
		},
		uuid.New(),
		nil,
	)
	require.NoError(t, err)

	payload := wokenTurnStartedPayload(t, fixture, sessionID)
	assert.Equal(t, origin.EventID.String(), payload.OriginEventID)
	assert.Equal(t, wakeTestHandlerType, payload.OriginEventType)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		sessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	for _, message := range messages.Items {
		if message.Content == wakeTestInstruction {
			assert.True(t, message.Injected, "a woken prompt is not typed")
		}
	}
}

// A background job that ends after its turn wakes the session when a handler
// asks for it, so an app that crashes gets looked at with nobody watching.
func TestBackgroundJobExitWakesTheSession(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{
		"command":    wakeTestCrashCommand,
		"purpose":    wakeTestCrashPurpose,
		"background": true,
	})
	require.NoError(t, err)

	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.ToolCall(wakeTestCrashCallID, toolNameRunCommand, string(arguments)),
		elelemtest.Text("started"),
		elelemtest.Text(wakeTestReply),
	))
	writeRuntimeFile(
		t,
		filepath.Join(
			fixture.configDirectory,
			".agents",
			"events",
			wakeTestJobEventType+".md",
		),
		wakeTestJobHandlerDocument,
	)

	sessionID := openWakeSession(t, fixture)

	_, err = fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &sessionID,
		Message:   wakeTestCrashPurpose,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		messages, listErr := fixture.store.ListMessages(
			context.Background(),
			sessionID,
			session.ListMessagesOptions{Order: session.PageOrderAscending},
		)

		return listErr == nil && containsContent(messages, wakeTestReply)
	}, wakeTestTimeout, wakeTestPoll, "the crashed job woke the session")
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

func TestPublishEventDuplicateIDDoesNotReplayOrFanOutTwice(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
	))
	sessionID := openWakeSession(t, fixture)
	notice := wakeTestNotice(sessionID)
	notice.ID = uuid.New()
	notice.Delivery = events.DeliveryQueue

	first, err := fixture.runtime.PublishEvent(context.Background(), notice)
	require.NoError(t, err)

	stream, stop := fixture.eventBus.Subscribe(sessionID)
	t.Cleanup(stop)

	_, err = fixture.runtime.PublishEvent(context.Background(), notice)
	require.ErrorIs(t, err, commerr.ErrAlreadyExists)
	assert.Equal(t, 1, fixture.eventBus.Pending(sessionID))
	assert.Empty(t, stream)

	page, err := fixture.store.ListSessionNotices(
		context.Background(),
		sessionID,
		session.ListSessionNoticesOptions{Limit: 10},
	)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, first.ID, page.Items[0].ID)
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

// runWakeCapturingError runs woken turns in the fixture's runtime and reports
// each turn's result on the returned channel, because the runtime only logs a
// failed woken turn.
func runWakeCapturingError(t *testing.T, fixture runtimeFixture) chan error {
	t.Helper()

	results := make(chan error, 1)

	fixture.runtime.SetWakeRunner(func(
		ctx context.Context,
		sessionID uuid.UUID,
		request MessageRequest,
	) error {
		err := fixture.runtime.runWakeTurn(ctx, sessionID, request)
		results <- err

		return err
	})

	return results
}

func waitForWakeResult(t *testing.T, results chan error) error {
	t.Helper()

	select {
	case err := <-results:
		return err
	case <-time.After(wakeTestTimeout):
		t.Fatal("the woken turn never finished")

		return nil
	}
}

// A handler body goes through the same path as a typed message, so a :name in
// it delivers that skill right after the handler message.
func TestPublishEventWakeDeliversASkillNamedInTheHandlerBody(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text(wakeTestReply),
	)
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)
	writeWakeHandler(
		t,
		fixture.configDirectory,
		wakeTestSkillHandlerDocument,
	)

	sessionID := openWakeSession(t, fixture)
	results := runWakeCapturingError(t, fixture)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)
	require.NoError(t, waitForWakeResult(t, results))

	requests := driver.Requests()
	require.Len(t, requests, 2)

	tail := tailTexts(requests[1].Messages, 3)
	assert.Contains(t, tail[0], "<handler>")
	assert.Contains(t, tail[0], ":"+runtimeTestExplicitSkillName)
	assert.Equal(
		t,
		expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
		tail[1],
	)
	assert.Contains(t, tail[2], sessionEventsOpenTag)
}

// An unknown name in a handler body fails the whole woken turn, as it fails a
// typed message. Nothing reaches the model.
func TestPublishEventWakeFailsWhenTheHandlerBodyNamesAnUnknownSkill(
	t *testing.T,
) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text("must not run"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeWakeHandler(
		t,
		fixture.configDirectory,
		wakeTestUnknownSkillHandlerDocument,
	)

	sessionID := openWakeSession(t, fixture)
	results := runWakeCapturingError(t, fixture)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	wakeErr := waitForWakeResult(t, results)
	require.ErrorIs(t, wakeErr, harness.ErrSkillNotFound)
	require.ErrorIs(t, wakeErr, commerr.ErrValidationFailed)
	assert.Len(t, driver.Requests(), 1)
}

func TestWakeMessageNamesTheHandlerAgent(t *testing.T) {
	t.Parallel()

	withoutAgent, err := wakeMessage(newWakeHandler(""))
	require.NoError(t, err)
	assert.Equal(t, wakeTestWrappedInstruction, withoutAgent)

	withAgent, err := wakeMessage(newWakeHandler("incident-responder"))
	require.NoError(t, err)
	assert.Equal(
		t,
		"<handler>\n<event>app.error</event>\n"+
			"<agent>incident-responder</agent>\n\n"+
			wakeTestInstruction+"\n\n"+
			"Handle this as the incident-responder agent would.\n"+
			"</handler>",
		withAgent,
	)
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
