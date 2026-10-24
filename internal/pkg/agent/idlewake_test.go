package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const idleWakeSettleWindow = time.Second

// holdSessionBusy acquires a turn lease the way a real turn in progress
// would, so the session reports active exactly as it does mid-turn, without
// needing a driver that blocks on a model call to make the race deterministic.
func holdSessionBusy(
	t *testing.T,
	fixture runtimeFixture,
	sessionID uuid.UUID,
) session.Lease {
	t.Helper()

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
	require.True(t, fixture.store.IsActive(sessionID))

	return lease
}

// A notice published while a turn is still running degrades to a queued
// delivery, because the session reports busy at that moment. This is the
// deterministic version of the race TestBackgroundJobExitWakesTheSession
// exercises through real timing: once the turn that was holding the session
// ends on its own, idle re-check must pick the queued notice back up and
// start exactly one wake turn.
func TestIdleWakeRecheckWakesTheSessionOnceItsHoldingTurnCompletes(
	t *testing.T,
) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text(wakeTestReply),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)
	lease := holdSessionBusy(t, fixture, sessionID)

	published, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)
	assert.Equal(t, events.DeliveryWake, published.Delivery)

	// Busy at publish time: considerWake degraded this to a queued delivery,
	// so nothing has started a second turn yet.
	assert.Never(t, func() bool {
		return fixture.eventBus.Pending(sessionID) == 0
	}, idleWakeSettleWindow, wakeTestPoll,
		"no turn starts while the session is still busy")

	require.NoError(t, fixture.store.FinalizeTurn(
		context.Background(),
		lease,
		session.FinalizeTurnInput{State: models.TurnStateCompleted},
	))

	require.Eventually(t, func() bool {
		messages, listErr := fixture.store.ListMessages(
			context.Background(),
			sessionID,
			session.ListMessagesOptions{Order: session.PageOrderAscending},
		)

		return listErr == nil && containsContent(messages, wakeTestReply)
	}, wakeTestTimeout, wakeTestPoll,
		"the idle re-check started exactly one wake turn for the queued notice")
}

// Stop cancels the holding turn instead of completing it. The idle re-check
// must not follow a cancellation with a fresh wake turn: the notice the
// cancelled turn never saw stays queued for whoever looks at the session
// next.
func TestIdleWakeRecheckDoesNotFollowACancelledTurn(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text("must not run"),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)
	lease := holdSessionBusy(t, fixture, sessionID)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	require.NoError(t, fixture.store.FinalizeTurn(
		context.Background(),
		lease,
		session.FinalizeTurnInput{State: models.TurnStateCancelled},
	))

	assert.Never(t, func() bool {
		messages, listErr := fixture.store.ListMessages(
			context.Background(),
			sessionID,
			session.ListMessagesOptions{Order: session.PageOrderAscending},
		)

		return listErr == nil && containsContent(messages, "must not run")
	}, idleWakeSettleWindow, wakeTestPoll,
		"a cancelled turn must not be followed by a fresh wake turn")

	page, err := fixture.store.ListSessionNotices(
		context.Background(),
		sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStatePending},
	)
	require.NoError(t, err)

	pendingTypes := make([]string, 0, len(page.Items))
	for _, notice := range page.Items {
		pendingTypes = append(pendingTypes, notice.Type)
	}

	// The wake notice stays queued, beside the record of the stopped turn.
	assert.ElementsMatch(t, []string{
		wakeTestHandlerType, events.TypeTurnCancelled,
	}, pendingTypes)
}

// A notice a tool boundary already delivered into the holding turn is no
// longer pending by the time that turn ends, so the idle re-check must not
// wake the session a second time for it.
func TestIdleWakeRecheckSkipsANoticeAlreadyDeliveredIntoTheTurn(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("session open"),
		elelemtest.Text("must not run"),
	))
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)
	lease := holdSessionBusy(t, fixture, sessionID)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	// A tool boundary inside the still-running turn drains the notice, the
	// same consumption a running turn performs on its own.
	_, err = fixture.store.DrainSessionNotices(context.Background(), sessionID)
	require.NoError(t, err)

	require.NoError(t, fixture.store.FinalizeTurn(
		context.Background(),
		lease,
		session.FinalizeTurnInput{State: models.TurnStateCompleted},
	))

	assert.Never(t, func() bool {
		messages, listErr := fixture.store.ListMessages(
			context.Background(),
			sessionID,
			session.ListMessagesOptions{Order: session.PageOrderAscending},
		)

		return listErr == nil && containsContent(messages, "must not run")
	}, idleWakeSettleWindow, wakeTestPoll,
		"a notice already delivered into the turn must not wake it again")
}

// The per-session wake rate limit applies to the idle re-check exactly as it
// applies to a live publish: a session already over its bound stays queued.
func TestIdleWakeRecheckLeavesTheSessionQueuedOverTheWakeRateLimit(t *testing.T) {
	fixture := newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(
			elelemtest.Text("session open"),
			elelemtest.Text("must not run"),
		),
		func(options *RuntimeOptions) {
			options.MaxEventWakesPerHour = 1
		},
	)
	writeWakeHandler(t, fixture.configDirectory, wakeTestHandlerDocument)

	sessionID := openWakeSession(t, fixture)

	// Spend the one wake this session is allowed before the race even
	// starts, exactly as a prior event's wake would have.
	require.True(t, fixture.runtime.wakes.allow(sessionID, time.Now()))

	lease := holdSessionBusy(t, fixture, sessionID)

	_, err := fixture.runtime.PublishEvent(
		context.Background(),
		wakeTestNotice(sessionID),
	)
	require.NoError(t, err)

	require.NoError(t, fixture.store.FinalizeTurn(
		context.Background(),
		lease,
		session.FinalizeTurnInput{State: models.TurnStateCompleted},
	))

	assert.Never(t, func() bool {
		messages, listErr := fixture.store.ListMessages(
			context.Background(),
			sessionID,
			session.ListMessagesOptions{Order: session.PageOrderAscending},
		)

		return listErr == nil && containsContent(messages, "must not run")
	}, idleWakeSettleWindow, wakeTestPoll,
		"a session over its wake limit stays queued after idle re-check")

	page, err := fixture.store.ListSessionNotices(
		context.Background(),
		sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStatePending},
	)
	require.NoError(t, err)
	assert.Len(t, page.Items, 1, "the rate-limited notice stays queued")
}
