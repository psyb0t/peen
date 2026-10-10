package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	queuedUserMessageInitial = "inspect the workspace"
	queuedUserMessageText    = "also inspect the tests"
	queuedUserMessageFinal   = "queued message handled"
)

func TestRuntimeQueuesUserMessageAtActiveTurnRoundBoundary(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) { options.MaxConcurrentTurns = 1 },
	)

	var (
		queued   *TurnResult
		queueErr error
		events   []Event
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			events = append(events, event)

			if event.Type == EventTypeToolUse {
				queued, queueErr = fixture.runtime.Run(
					context.Background(),
					TurnRequest{
						Message: queuedUserMessageText,
					},
				)
			}

			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, queueErr)
	require.NotNil(t, queued)
	assert.True(t, queued.Queued)
	assert.Equal(t, result.SessionID, queued.SessionID)
	assert.Equal(t, queuedUserMessageFinal, result.Text)
	assert.Equal(
		t,
		[]string{
			EventTypeUserMessageCreated,
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeUserMessageCreated,
			EventTypeUserMessageQueued,
			EventTypeToolResult,
			EventTypeUserMessageDelivered,
			EventTypeTurnCompleted,
		},
		harnessEventTypes(events),
	)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, elelem.RoleUser, requests[1].Messages[len(requests[1].Messages)-1].Role)
	assert.Equal(t, queuedUserMessageText, requests[1].Messages[len(requests[1].Messages)-1].Text())

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]models.MessageRole{
			models.MessageRoleUser,
			models.MessageRoleAssistant,
			models.MessageRoleTool,
			models.MessageRoleUser,
			models.MessageRoleAssistant,
		},
		messageRoles(messages.Items),
	)
	assert.Equal(t, queuedUserMessageText, messages.Items[3].Content)

	query := repositories.Use(fixture.handle.GormDB)
	auditEvents, err := query.Event.WithContext(context.Background()).
		Where(query.Event.SessionID.Eq(result.SessionID)).
		Where(query.Event.EventType.Eq(EventTypeUserMessageQueued)).
		Find()
	require.NoError(t, err)
	require.Len(t, auditEvents, 1)

	payload := userMessagePayload{}
	require.NoError(t, json.Unmarshal([]byte(auditEvents[0].PayloadJSON), &payload))
	assert.Equal(t, queuedUserMessageText, payload.Message)

	createdEvents, err := query.Event.WithContext(context.Background()).
		Where(query.Event.SessionID.Eq(result.SessionID)).
		Where(query.Event.EventType.Eq(EventTypeUserMessageCreated)).
		Find()
	require.NoError(t, err)
	require.Len(t, createdEvents, 2)
}

// Several messages sent during one tool call all reach the model's next
// request in the order they were sent, and each one reports its delivery
// under its own request ID so every client can show it landing.
func TestRuntimeDeliversSeveralQueuedUserMessagesInOrder(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) { options.MaxConcurrentTurns = 1 },
	)

	queuedTexts := []string{"first follow-up", "second follow-up", "third follow-up"}
	queuedRequestIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	var (
		queueErrs []error
		events    []Event
	)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			events = append(events, event)

			if event.Type != EventTypeToolUse {
				return nil
			}

			for index, text := range queuedTexts {
				queued, queueErr := fixture.runtime.Run(
					context.Background(),
					TurnRequest{Message: text, RequestID: queuedRequestIDs[index]},
				)
				queueErrs = append(queueErrs, queueErr)

				if queueErr == nil {
					assert.True(t, queued.Queued)
				}
			}

			return nil
		},
	})
	require.NoError(t, err)

	for _, queueErr := range queueErrs {
		require.NoError(t, queueErr)
	}

	requests := driver.Requests()
	require.Len(t, requests, 2)

	sent := requests[1].Messages
	require.GreaterOrEqual(t, len(sent), len(queuedTexts))

	for index, text := range queuedTexts {
		message := sent[len(sent)-len(queuedTexts)+index]
		assert.Equal(t, elelem.RoleUser, message.Role)
		assert.Equal(t, text, message.Text())
	}

	var (
		deliveredRequestIDs []uuid.UUID
		deliveredTexts      []string
	)

	toolResultIndex := -1

	for index, event := range events {
		switch event.Type {
		case EventTypeToolResult:
			toolResultIndex = index
		case EventTypeUserMessageDelivered:
			assert.Greater(t, toolResultIndex, -1, "delivered before the tool call ended")

			payload := userMessagePayload{}
			require.NoError(t, json.Unmarshal(event.Payload, &payload))
			deliveredRequestIDs = append(deliveredRequestIDs, event.RequestID)
			deliveredTexts = append(deliveredTexts, payload.Message)
		}
	}

	assert.Equal(t, queuedRequestIDs, deliveredRequestIDs)
	assert.Equal(t, queuedTexts, deliveredTexts)
}

func TestRuntimeRunMessageQueuesActiveTurn(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixture(t, driver)

	var (
		queued   *MessageRunResult
		queueErr error
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			if event.Type == EventTypeToolUse {
				queued, queueErr = fixture.runtime.RunMessage(
					context.Background(),
					MessageRequest{Message: queuedUserMessageText},
					uuid.New(),
					nil,
				)
			}

			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, queueErr)
	require.NotNil(t, queued)
	assert.True(t, queued.Queued)
	assert.Equal(t, result.SessionID, queued.SessionID)
	assert.Equal(t, queuedUserMessageFinal, result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, queuedUserMessageText, requests[1].Messages[len(requests[1].Messages)-1].Text())
}

func TestRuntimeRejectsFullActiveUserMessageQueue(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) { options.MaxQueuedUserMessages = 1 },
	)

	var (
		first     *TurnResult
		firstErr  error
		secondErr error
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			if event.Type == EventTypeToolUse {
				first, firstErr = fixture.runtime.Run(
					context.Background(),
					TurnRequest{Message: queuedUserMessageText},
				)
				_, secondErr = fixture.runtime.Run(
					context.Background(),
					TurnRequest{Message: "one message too many"},
				)
			}

			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, firstErr)
	require.NotNil(t, first)
	assert.True(t, first.Queued)
	require.Error(t, secondErr)
	assert.ErrorIs(t, secondErr, commerr.ErrConflict)
	assert.ErrorIs(t, secondErr, elelem.ErrUserMessageQueueFull)
	assert.Equal(t, queuedUserMessageFinal, result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, queuedUserMessageText, requests[1].Messages[len(requests[1].Messages)-1].Text())
}

const queuedSkillMessage = "when done, follow :review-rules"

// queueDuringToolCall runs a turn that calls one tool and sends each message
// in sent to the running turn at that call. It returns the turn result, every
// event the first turn published, and each queued message's own outcome.
func queueDuringToolCall(
	t *testing.T,
	fixture runtimeFixture,
	sent []TurnRequest,
	initial string,
) (*TurnResult, []Event, []queueOutcome) {
	t.Helper()

	var (
		events   []Event
		outcomes []queueOutcome
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   initial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			events = append(events, event)

			if event.Type != EventTypeToolUse {
				return nil
			}

			for _, request := range sent {
				queued, queueErr := fixture.runtime.Run(
					context.Background(),
					request,
				)
				outcomes = append(outcomes, queueOutcome{
					result: queued,
					err:    queueErr,
				})
			}

			return nil
		},
	})
	require.NoError(t, err)

	return result, events, outcomes
}

type queueOutcome struct {
	result *TurnResult
	err    error
}

func listFilesThenAnswerDriver() *elelemtest.ScriptedDriver {
	return elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
}

// A skill named in a message sent during a running turn reaches the model as
// its own message right after that message, together with it, after the
// current tool call.
func TestRuntimeQueuedSkillReferenceDeliversTheSkillMessageWithTheUserMessage(
	t *testing.T,
) {
	driver := listFilesThenAnswerDriver()
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	requestID := uuid.New()
	result, events, outcomes := queueDuringToolCall(
		t,
		fixture,
		[]TurnRequest{{Message: queuedSkillMessage, RequestID: requestID}},
		queuedUserMessageInitial,
	)

	require.Len(t, outcomes, 1)
	require.NoError(t, outcomes[0].err)
	assert.True(t, outcomes[0].result.Queued)
	assert.Equal(t, queuedUserMessageFinal, result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(
		t,
		[]string{
			queuedSkillMessage,
			expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
		},
		tailTexts(requests[1].Messages, 2),
	)
	assert.Equal(t, requests[0].Messages[0].Text(), requests[1].Messages[0].Text())
	assert.NotContains(t, requests[1].Messages[0].Text(), skillTestBravoBody)

	delivered := 0

	for _, event := range events {
		if event.Type != EventTypeUserMessageDelivered {
			continue
		}

		delivered++

		assert.Equal(t, requestID, event.RequestID)
	}

	assert.Equal(t, 1, delivered)
	assert.Equal(
		t,
		[]string{
			EventTypeUserMessageCreated,
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeUserMessageCreated,
			EventTypeUserMessageQueued,
			EventTypeToolResult,
			EventTypeUserMessageDelivered,
			EventTypeSkillInjected,
			EventTypeTurnCompleted,
		},
		harnessEventTypes(events),
	)

	stored, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	require.Len(t, stored.Items, 6)
	assert.Equal(t, queuedSkillMessage, stored.Items[3].Content)
	assert.False(t, stored.Items[3].Injected)
	assert.Equal(
		t,
		expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
		stored.Items[4].Content,
	)
	assert.True(t, stored.Items[4].Injected)
	assert.Equal(t, models.InjectedKindSkill, stored.Items[4].InjectedKind)
}

// The message is refused as invalid input, like at the start of a turn, and
// leaves no trace: nothing queued, no event, nothing stored.
func TestRuntimeQueuedUnknownSkillReferenceRefusesTheMessage(t *testing.T) {
	driver := listFilesThenAnswerDriver()
	fixture := newRuntimeFixture(t, driver)

	result, events, outcomes := queueDuringToolCall(
		t,
		fixture,
		[]TurnRequest{{Message: "then use :missing-skill"}},
		queuedUserMessageInitial,
	)

	require.Len(t, outcomes, 1)
	require.ErrorIs(t, outcomes[0].err, harness.ErrSkillNotFound)
	require.ErrorIs(t, outcomes[0].err, commerr.ErrValidationFailed)

	var unknown *harness.UnknownSkillError
	require.ErrorAs(t, outcomes[0].err, &unknown)
	assert.Equal(
		t,
		`unknown skill "missing-skill" in :missing-skill; `+
			`available skills: core:freshness, core:planning`,
		unknown.Message,
	)
	assert.Equal(t, queuedUserMessageFinal, result.Text)

	for _, event := range events {
		assert.NotEqual(t, EventTypeUserMessageQueued, event.Type)
		assert.NotEqual(t, EventTypeUserMessageDelivered, event.Type)
	}

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, len(requests[0].Messages)+2, len(requests[1].Messages))

	stored, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	assert.Len(t, stored.Items, 4)
}

// Each skill message takes a queue slot, so a message that brings skills
// leaves less room, and a message that no longer fits is refused whole.
func TestRuntimeQueuedSkillMessagesCountAgainstTheQueueBound(t *testing.T) {
	const queueBound = 2

	driver := listFilesThenAnswerDriver()
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) {
			options.MaxQueuedUserMessages = queueBound
		},
	)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	result, _, outcomes := queueDuringToolCall(
		t,
		fixture,
		[]TurnRequest{
			{Message: queuedSkillMessage},
			{Message: queuedUserMessageText},
		},
		queuedUserMessageInitial,
	)

	require.Len(t, outcomes, 2)
	require.NoError(t, outcomes[0].err)
	require.ErrorIs(t, outcomes[1].err, elelem.ErrUserMessageQueueFull)
	assert.Equal(t, queuedUserMessageFinal, result.Text)

	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(
		t,
		[]string{
			queuedSkillMessage,
			expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
		},
		tailTexts(requests[1].Messages, 2),
	)
}

// A skill the turn's own first message already brought is not repeated by a
// queued message that names it, and neither is one an earlier queued message
// brought.
func TestRuntimeQueuedSkillReferenceNotesASkillAlreadyLoaded(t *testing.T) {
	driver := listFilesThenAnswerDriver()
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	const second = "and once more :review-rules"

	_, _, outcomes := queueDuringToolCall(
		t,
		fixture,
		[]TurnRequest{{Message: queuedSkillMessage}, {Message: second}},
		runtimeTestExplicitSkillMessage,
	)

	require.Len(t, outcomes, 2)
	require.NoError(t, outcomes[0].err)
	require.NoError(t, outcomes[1].err)

	requests := driver.Requests()
	require.Len(t, requests, 2)

	note := "<note>Skill review-rules is already loaded earlier in this " +
		"conversation.</note>"
	assert.Equal(
		t,
		[]string{queuedSkillMessage, note, second, note},
		tailTexts(requests[1].Messages, 4),
	)
}

func TestQueuedUserMessageCountAtEndMatchesSkillMessages(t *testing.T) {
	t.Parallel()

	skill := skillMessage{name: "x", text: "<skill>x</skill>"}
	pending := []queuedUserMessage{
		{message: "first", skills: []skillMessage{skill}},
		{message: "second"},
	}

	transcript := func(texts ...string) []elelem.Message {
		messages := make([]elelem.Message, 0, len(texts)+1)
		messages = append(messages, elelem.Message{
			Role:    elelem.RoleAssistant,
			Content: elelem.Text("earlier"),
		})

		for _, text := range texts {
			messages = append(messages, elelem.Message{
				Role:    elelem.RoleUser,
				Content: elelem.Text(text),
			})
		}

		return messages
	}

	testCases := []struct {
		name     string
		messages []elelem.Message
		want     int
	}{
		{
			name:     "both delivered",
			messages: transcript("first", skill.text, "second"),
			want:     2,
		},
		{
			name:     "only the first with its skill",
			messages: transcript("first", skill.text),
			want:     1,
		},
		{
			name:     "a user message without its skill message",
			messages: transcript("first"),
			want:     0,
		},
		{
			name:     "none delivered",
			messages: transcript("something else"),
			want:     0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(
				t,
				tc.want,
				queuedUserMessageCountAtEnd(tc.messages, pending),
			)
		})
	}
}

func TestRuntimeIgnoresActiveTurnWorkspaceOverride(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	))

	var (
		queued   *TurnResult
		queueErr error
	)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   queuedUserMessageInitial,
		Workspace: fixture.workspace,
		OnEvent: func(event Event) error {
			if event.Type == EventTypeToolUse {
				queued, queueErr = fixture.runtime.Run(
					context.Background(),
					TurnRequest{
						Message:   queuedUserMessageText,
						Workspace: fixture.otherWorkspace,
					},
				)
			}

			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, queueErr)
	require.NotNil(t, queued)
	assert.True(t, queued.Queued)
	assert.Equal(t, fixture.runtime.SessionID(), queued.SessionID)
}

// A control surface sends the selected model and reasoning with every
// message. A message that repeats the running turn's own settings changes
// nothing, so it joins the queue; one that names another model is refused as
// invalid input rather than as a busy session.
func TestRuntimeQueuesAMessageThatRepeatsTheRunningTurnSettings(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixture(t, driver)

	var (
		queued     *TurnResult
		queueErr   error
		changedErr error
	)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:         queuedUserMessageInitial,
		ReasoningEffort: elelem.ReasoningEffortHigh,
		OnEvent: func(event Event) error {
			if event.Type != EventTypeToolUse {
				return nil
			}

			// The running turn named no model, so it runs the default. The
			// qualified default is the same model, not a change.
			queued, queueErr = fixture.runtime.Run(
				context.Background(),
				TurnRequest{
					Message:         queuedUserMessageText,
					Model:           runtimeTestModelReference,
					ReasoningEffort: elelem.ReasoningEffortHigh,
				},
			)
			_, changedErr = fixture.runtime.Run(
				context.Background(),
				TurnRequest{
					Message:         "switch to another model",
					Model:           "other/model",
					ReasoningEffort: elelem.ReasoningEffortHigh,
				},
			)

			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, queueErr)
	require.NotNil(t, queued)
	assert.True(t, queued.Queued)
	require.ErrorIs(t, changedErr, commerr.ErrValidationFailed)
	require.ErrorIs(t, changedErr, session.ErrRunningTurnSettingsChange)

	requests := driver.Requests()
	require.Len(t, requests, 2)

	delivered := requests[1].Messages[len(requests[1].Messages)-1]
	assert.Equal(t, queuedUserMessageText, delivered.Text())
	assert.Equal(t, elelem.ReasoningEffortHigh, requests[1].Params.ReasoningEffort)
}

// gatedDriver holds the first provider call until the test opens its gate, so
// the turn that made the call stays running while the test watches what
// happened to the other messages.
type gatedDriver struct {
	*elelemtest.ScriptedDriver

	gate     chan struct{}
	gateOnce sync.Once
}

func newGatedDriver(turns ...elelemtest.Turn) *gatedDriver {
	return &gatedDriver{
		ScriptedDriver: elelemtest.NewScriptedDriver(turns...),
		gate:           make(chan struct{}),
	}
}

func (d *gatedDriver) Stream(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	d.waitForGate(ctx)

	return d.ScriptedDriver.Stream(ctx, request, onDelta)
}

func (d *gatedDriver) Complete(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	d.waitForGate(ctx)

	return d.ScriptedDriver.Complete(ctx, request, onDelta)
}

func (d *gatedDriver) waitForGate(ctx context.Context) {
	d.gateOnce.Do(func() {
		select {
		case <-d.gate:
		case <-ctx.Done():
		}
	})
}

// Two messages that reach an idle session at the same moment must not race
// each other into a busy refusal. One of them starts the turn and the other
// joins that turn's queue, even when it arrives while the first is still
// taking the session lease and before its queue exists.
func TestRuntimeAdmitsConcurrentMessagesToAnIdleSession(t *testing.T) {
	driver := newGatedDriver(
		elelemtest.Text("first answer"),
		elelemtest.Text(queuedUserMessageFinal),
	)
	fixture := newRuntimeFixture(t, driver)

	type outcome struct {
		message string
		result  *TurnResult
		err     error
	}

	messages := []string{"first concurrent message", "second concurrent message"}
	outcomes := make(chan outcome, len(messages))
	start := make(chan struct{})

	for _, message := range messages {
		go func() {
			<-start

			result, err := fixture.runtime.Run(
				context.Background(),
				TurnRequest{Message: message},
			)
			outcomes <- outcome{message: message, result: result, err: err}
		}()
	}

	close(start)

	// The queued message answers at once. The started turn cannot answer
	// until its held provider call is released, so it is always second.
	queued := <-outcomes
	close(driver.gate)
	started := <-outcomes

	require.NoError(t, queued.err)
	require.NoError(t, started.err)
	assert.True(t, queued.result.Queued)
	assert.False(t, started.result.Queued)

	requests := driver.Requests()
	require.Len(t, requests, 2)

	opening := requests[0].Messages[len(requests[0].Messages)-1]
	assert.Equal(t, started.message, opening.Text())

	delivered := requests[1].Messages[len(requests[1].Messages)-1]
	assert.Equal(t, queued.message, delivered.Text())
}
