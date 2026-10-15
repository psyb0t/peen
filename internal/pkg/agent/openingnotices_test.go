package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type openingAdmissionFailureStore struct {
	*session.Store
}

func (s *openingAdmissionFailureStore) AcquireTurn(
	_ context.Context,
	_ uuid.UUID,
	_ session.StartTurnInput,
) (session.Lease, error) {
	return session.Lease{}, ctxerrors.Wrap(commerr.ErrConflict,
		"injected turn admission refusal")
}

func TestRuntimeAdmissionFailureRetainsOpeningNotices(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("continued"))
	fixture := newRuntimeFixture(t, driver)
	opened, err := fixture.store.OpenWorkspace(t.Context(), fixture.workspace,
		session.OpenSessionOptions{
			RootAgent: runtimeTestAgentName, ModelID: runtimeTestModelReference,
		})
	require.NoError(t, err)
	sessionID := opened.Session.ID
	notice, err := fixture.runtime.PublishEvent(t.Context(), events.Notice{
		SessionID: sessionID, Type: "app.ready", Source: "example",
		Summary: "retained opening report", Delivery: events.DeliveryQueue,
	})
	require.NoError(t, err)
	before, err := fixture.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	fixture.runtime.store = &openingAdmissionFailureStore{Store: fixture.store}
	input := TurnRequest{
		SessionID: &sessionID, RequestID: uuid.New(),
		Workspace: fixture.workspace, Message: "continue",
	}
	_, err = fixture.runtime.Run(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.Zero(t, driver.Calls())
	assert.False(t, fixture.store.IsActive(sessionID))
	after, err := fixture.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, 1, fixture.eventBus.Pending(sessionID))
	fixture.runtime.store = fixture.store
	result, err := fixture.runtime.Run(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "continued", result.Text)
	assert.Equal(t, 1, driver.Calls())
	requests := driver.Requests()
	require.Len(t, requests, 1)
	texts := make([]string, 0, len(requests[0].Messages))
	for _, message := range requests[0].Messages {
		texts = append(texts, message.Text())
	}
	assert.Contains(t, strings.Join(texts, "\n"), notice.ID.String())
	assert.Contains(t, strings.Join(texts, "\n"), notice.Summary)
	after, err = fixture.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, after.Items, 1)
	assert.Equal(t, models.NoticeStateDelivered, after.Items[0].State)
	assert.Zero(t, fixture.eventBus.Pending(sessionID))
}

func TestDeferredOpeningRetainsOriginalBatchAndNewerNotice(t *testing.T) {
	t.Parallel()
	fixture, message, _ := deferredRootFixture(t)
	runtime := fixture.base.runtime
	sessionID := message.SessionID
	firstNotice, err := runtime.PublishEvent(t.Context(), events.Notice{
		SessionID: sessionID, Type: "app.ready", Source: "example",
		Summary: "original opening report", Delivery: events.DeliveryQueue,
	})
	require.NoError(t, err)
	var delivered []Event
	sink := func(event Event) error {
		delivered = append(delivered, event)

		return nil
	}
	first, err := runtime.prepareDeferredRoot(t.Context(), message.ExecutionID,
		message.RequestID, sink)
	require.NoError(t, err)
	fixture.base.store.ReleaseTurn(first.lease)
	before, err := fixture.base.store.ListMessages(t.Context(), sessionID,
		session.ListMessagesOptions{})
	require.NoError(t, err)
	beforeEvents, err := fixture.base.store.ListEvents(t.Context(), sessionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	newer, err := runtime.PublishEvent(t.Context(), events.Notice{
		SessionID: sessionID, Type: "app.ready", Source: "example",
		Summary: "newer opening report", Delivery: events.DeliveryQueue,
	})
	require.NoError(t, err)
	second, err := runtime.prepareDeferredRoot(t.Context(), message.ExecutionID,
		message.RequestID, sink)
	require.NoError(t, err)
	t.Cleanup(func() { fixture.base.store.ReleaseTurn(second.lease) })
	assert.Equal(t, first.lease.TurnID, second.lease.TurnID)
	assert.Equal(t, first.pendingEvents, second.pendingEvents)
	assert.Contains(t, second.pendingEvents, firstNotice.ID.String())
	assert.NotContains(t, second.pendingEvents, newer.ID.String())
	messages := second.prompt.Messages()
	texts := make([]string, 0, len(messages))
	for _, row := range messages {
		texts = append(texts, row.Text())
	}
	assert.Contains(t, strings.Join(texts, "\n"), firstNotice.ID.String())
	assert.NotContains(t, strings.Join(texts, "\n"), newer.ID.String())
	assert.Empty(t, second.turn.events)
	questionDeliveries := 0
	for _, event := range delivered {
		if event.Type == EventTypeSessionEvents {
			questionDeliveries++
			assert.Contains(t, string(event.Payload), firstNotice.ID.String())
			assert.NotContains(t, string(event.Payload), newer.ID.String())
		}
	}
	assert.Equal(t, 1, questionDeliveries)
	after, err := fixture.base.store.ListMessages(t.Context(), sessionID,
		session.ListMessagesOptions{})
	require.NoError(t, err)
	assert.Equal(t, before, after)
	afterEvents, err := fixture.base.store.ListEvents(t.Context(), sessionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	assert.Equal(t, beforeEvents, afterEvents)
	pending, err := fixture.base.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, pending.Items, 1)
	assert.Equal(t, newer.ID, pending.Items[0].ID)
	bus := fixture.base.eventBus.Peek(sessionID)
	require.Len(t, bus.Notices, 1)
	assert.Equal(t, newer.ID, bus.Notices[0].ID)
}

func TestOpeningPreviewPagesDoNotConsumeNotices(t *testing.T) {
	t.Parallel()
	fixture := inboxTestFixture(t)
	sessionID, _ := inboxSession(t, fixture)
	ids := make([]uuid.UUID, 0, session.DefaultPageLimit+3)
	for range cap(ids) {
		notice, err := fixture.store.CreateSessionNotice(t.Context(), sessionID,
			session.CreateSessionNoticeInput{
				Type: "app.ready", Source: "example", Summary: "ready",
				DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
			})
		require.NoError(t, err)
		ids = append(ids, notice.ID)
	}
	batch, err := fixture.runtime.peekOpeningEvents(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, ids, noticeBatchIDs(batch))
	count, err := fixture.runtime.pendingSessionNoticeCount(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, len(ids), count)
	fixture.runtime.store = &noticeReadFailureStore{Store: fixture.store}
	_, err = fixture.runtime.peekOpeningEvents(t.Context(), sessionID)
	require.ErrorIs(t, err, commerr.ErrConflict)
	fixture.runtime.store = fixture.store
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = fixture.runtime.peekOpeningEvents(ctx, sessionID)
	require.ErrorIs(t, err, context.Canceled)
}

func TestOpeningAdmissionHonorsCancellationAtGate(t *testing.T) {
	t.Parallel()
	runtime := &Runtime{}
	release, err := runtime.noticeGate.acquire(t.Context(), true)
	require.NoError(t, err)
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = runtime.admitRootOpening(ctx, uuid.New(),
		session.StartTurnInput{}, &turnOpening{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestRuntimeWakeRefusalRetainsNoticesWithoutProviderCall(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("continued"))
	fixture := newRuntimeFixture(t, driver)
	sessionID, sourceLease, run := runtimeWakeSource(t, fixture)
	data, err := json.Marshal(events.AgentNoticeData{RunID: run.ID})
	require.NoError(t, err)
	notice, err := fixture.runtime.persistAndPublishNotice(t.Context(),
		events.Notice{
			SessionID: sessionID, Type: events.TypeAgentQuestion,
			Source: events.AgentSource, Summary: "child needs input",
			Data: data, Delivery: events.DeliveryWake,
		})
	require.NoError(t, err)
	_, _, err = fixture.store.RequestAgentRunCancellation(t.Context(), sessionID,
		run.ID)
	require.NoError(t, err)
	fixture.store.ReleaseTurn(sourceLease)
	input := TurnRequest{
		SessionID: &sessionID, RequestID: uuid.New(),
		Workspace: fixture.workspace, Message: "handle child event",
		Origin: &TurnOrigin{EventID: notice.ID, EventType: notice.Type},
	}
	_, err = fixture.runtime.Run(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.Zero(t, driver.Calls())
	assert.False(t, fixture.store.IsActive(sessionID))
	assert.Equal(t, 1, fixture.eventBus.Pending(sessionID))
	page, err := fixture.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, notice.ID, page.Items[0].ID)
	input.Origin = nil
	input.Message = "new unrelated task"
	result, err := fixture.runtime.Run(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "continued", result.Text)
	assert.Equal(t, 1, driver.Calls())
	stopped, err := fixture.store.GetAgentRun(t.Context(), sessionID, run.ID)
	require.NoError(t, err)
	assert.True(t, stopped.CancelRequested)
}

func runtimeWakeSource(t *testing.T, fixture runtimeFixture) (
	uuid.UUID, session.Lease, *models.AgentRun,
) {
	t.Helper()
	opened, err := fixture.store.OpenWorkspace(t.Context(), fixture.workspace,
		session.OpenSessionOptions{
			RootAgent: runtimeTestAgentName,
			ModelID:   runtimeTestModelReference,
		})
	require.NoError(t, err)
	sessionID := opened.Session.ID
	lease, err := fixture.store.AcquireTurn(t.Context(), sessionID,
		session.StartTurnInput{RequestID: uuid.New(), Workspace: fixture.workspace})
	require.NoError(t, err)
	run, err := fixture.store.CreateAgentRun(t.Context(), sessionID,
		session.StartAgentRunInput{
			ID: uuid.New(), ParentTurnID: lease.TurnID,
			RequestID: uuid.New(), Name: "exampleChild", Depth: 1,
			Definition: models.AgentRunDefinitionAdHoc, Workspace: fixture.workspace,
			ModelReference: runtimeTestModelReference, ModelID: runtimeTestModelReference,
			Task: "inspect files", Instructions: "read workspace",
			AllowedToolsJSON: `[]`, SystemPrompt: "inspect workspace",
		})
	require.NoError(t, err)

	return sessionID, lease, run
}

func TestRuntimeLegacyChildWakeRetainsNoticeContext(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("continued"))
	fixture := newRuntimeFixture(t, driver)
	sessionID, sourceLease, run := runtimeWakeSource(t, fixture)
	fixture.store.ReleaseTurn(sourceLease)
	data, err := json.Marshal(events.AgentNoticeData{RunID: run.ID})
	require.NoError(t, err)
	notice, err := fixture.runtime.persistAndPublishNotice(t.Context(),
		events.Notice{
			SessionID: sessionID, Type: events.TypeAgentQuestion,
			Source: events.AgentSource, Summary: "child needs input",
			Data: data, Delivery: events.DeliveryWake,
		})
	require.NoError(t, err)
	result, err := fixture.runtime.Run(t.Context(), TurnRequest{
		SessionID: &sessionID, RequestID: uuid.New(), Workspace: fixture.workspace,
		Message: "handle child event",
		Origin:  &TurnOrigin{EventID: notice.ID, EventType: notice.Type},
	})
	require.NoError(t, err)
	assert.Equal(t, "continued", result.Text)
	requests := driver.Requests()
	require.Len(t, requests, 1)
	texts := make([]string, 0, len(requests[0].Messages))
	for _, message := range requests[0].Messages {
		texts = append(texts, message.Text())
	}
	assert.Contains(t, strings.Join(texts, "\n"), notice.ID.String())
	assert.Zero(t, fixture.eventBus.Pending(sessionID))
	page, err := fixture.store.ListSessionNotices(t.Context(), sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStateDelivered})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, notice.ID, page.Items[0].ID)
}
