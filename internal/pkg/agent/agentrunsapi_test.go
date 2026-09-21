package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentRunFixture is one opened session plus its first turn, which is the
// parent every child run needs.
type agentRunFixture struct {
	runtime   *Runtime
	store     *session.Store
	sessionID uuid.UUID
	turnID    uuid.UUID
	workspace string
}

func newAgentRunFixture(t *testing.T) agentRunFixture {
	t.Helper()

	ctx := context.Background()
	fixture := newRuntimeFixture(
		t,
		elelemtest.NewScriptedDriver(elelemtest.Text("done")),
	)

	turnResult, err := fixture.runtime.Run(ctx, TurnRequest{
		Message:   "open the session",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	query := repositories.Use(fixture.handle.GormDB)
	turn, err := query.Turn.WithContext(ctx).
		Where(query.Turn.SessionID.Eq(turnResult.SessionID)).
		First()
	require.NoError(t, err)

	return agentRunFixture{
		runtime:   fixture.runtime,
		store:     fixture.store,
		sessionID: turnResult.SessionID,
		turnID:    turn.ID,
		workspace: fixture.workspace,
	}
}

func (f agentRunFixture) startRun(
	t *testing.T,
	name string,
	allowedToolsJSON string,
) *models.AgentRun {
	t.Helper()

	run, err := f.store.CreateAgentRun(
		context.Background(),
		f.sessionID,
		session.StartAgentRunInput{
			ID:                 uuid.New(),
			ParentTurnID:       f.turnID,
			WorkerGenerationID: uuid.New().String(),
			ParentToolCallID:   "call-" + name,
			RequestID:          uuid.New(),
			Name:               name,
			Definition:         models.AgentRunDefinitionStored,
			Depth:              1,
			Workspace:          f.workspace,
			ModelReference:     "scripted/test-model",
			ModelID:            "test-model",
			Task:               "investigate " + name,
			Instructions:       "be brief",
			AllowedToolsJSON:   allowedToolsJSON,
			SystemPrompt:       "you are " + name,
			StartedAt:          time.Now().UTC(),
		},
	)
	require.NoError(t, err)

	return run
}

// A session that never launched a child answers with an empty page rather than
// an error, which is what lets a client render the tab unconditionally.
func TestRuntimeListsNoAgentRunsForASessionThatLaunchedNone(t *testing.T) {
	fixture := newAgentRunFixture(t)

	page, err := fixture.runtime.ListSessionAgentRuns(
		context.Background(),
		fixture.sessionID,
		api.ListSessionAgentRunsParams{},
	)
	require.NoError(t, err)
	require.NotNil(t, page)
	assert.Empty(t, page.Agents)
	assert.False(t, page.HasMore)
}

// The durable list is newest first and pages by limit/offset, and the state
// filter narrows it to one lifecycle state.
func TestRuntimeListsAgentRunsNewestFirstWithPagingAndStateFilter(t *testing.T) {
	ctx := context.Background()
	fixture := newAgentRunFixture(t)

	first := fixture.startRun(t, "first", `["read_file"]`)
	second := fixture.startRun(t, "second", `[]`)

	_, err := fixture.store.FinalizeAgentRun(
		ctx,
		fixture.sessionID,
		first.ID,
		session.FinalizeAgentRunInput{
			State:                models.AgentRunStateCompleted,
			ResponseText:         "found it",
			ResponseMessagesJSON: `[{"role":"assistant"}]`,
			FinishReason:         "stop",
			PromptTokenCount:     11,
			CompletionTokenCount: 22,
		},
	)
	require.NoError(t, err)

	page, err := fixture.runtime.ListSessionAgentRuns(
		ctx,
		fixture.sessionID,
		api.ListSessionAgentRunsParams{},
	)
	require.NoError(t, err)
	require.Len(t, page.Agents, 2)
	assert.Equal(t, second.ID, page.Agents[0].AgentRunId)
	assert.Equal(t, first.ID, page.Agents[1].AgentRunId)

	limit := int32(1)
	offset := int32(1)
	secondPage, err := fixture.runtime.ListSessionAgentRuns(
		ctx,
		fixture.sessionID,
		api.ListSessionAgentRunsParams{Limit: &limit, Offset: &offset},
	)
	require.NoError(t, err)
	require.Len(t, secondPage.Agents, 1)
	assert.Equal(t, first.ID, secondPage.Agents[0].AgentRunId)
	assert.Equal(t, limit, secondPage.Limit)
	assert.Equal(t, offset, secondPage.Offset)
	assert.False(t, secondPage.HasMore)

	state := api.ListSessionAgentRunsParamsState(models.AgentRunStateCompleted)
	filtered, err := fixture.runtime.ListSessionAgentRuns(
		ctx,
		fixture.sessionID,
		api.ListSessionAgentRunsParams{State: &state},
	)
	require.NoError(t, err)
	require.Len(t, filtered.Agents, 1)
	assert.Equal(t, first.ID, filtered.Agents[0].AgentRunId)
}

// Every durable column a client reads has to survive the model-to-API
// conversion, including the JSON payload columns and the optional pointers.
func TestRuntimeGetsOneAgentRunWithItsDurableDetail(t *testing.T) {
	ctx := context.Background()
	fixture := newAgentRunFixture(t)
	run := fixture.startRun(t, "detail", `["read_file","run_command"]`)

	_, err := fixture.store.FinalizeAgentRun(
		ctx,
		fixture.sessionID,
		run.ID,
		session.FinalizeAgentRunInput{
			State:                 models.AgentRunStateFailed,
			ResponseText:          "gave up",
			ResponseThinking:      "considered it",
			ResponseMessagesJSON:  `[{"role":"assistant"},{"role":"user"}]`,
			FinishReason:          "error",
			FailureClassification: "model_error",
			FailureDetail:         "upstream refused",
		},
	)
	require.NoError(t, err)

	got, err := fixture.runtime.GetSessionAgentRun(ctx, fixture.sessionID, run.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, run.ID, got.AgentRunId)
	assert.Equal(t, fixture.sessionID, got.SessionId)
	assert.Equal(t, fixture.turnID, got.ParentTurnId)
	assert.Equal(t, api.AgentRunState(models.AgentRunStateFailed), got.State)
	assert.Equal(t, "gave up", got.ResponseText)
	assert.Equal(t, "considered it", got.ResponseThinking)
	assert.Equal(t, "model_error", got.FailureClassification)
	assert.Equal(t, "upstream refused", got.FailureDetail)
	assert.Len(t, got.AllowedTools, 2)
	assert.Len(t, got.ResponseMessages, 2)
	require.NotNil(t, got.ParentToolCallId)
	assert.Equal(t, "call-detail", *got.ParentToolCallId)
	require.NotNil(t, got.WorkerGenerationId)
	assert.NotEmpty(t, *got.WorkerGenerationId)
	require.NotNil(t, got.EndedAt)
}

// An unknown run is a not-found read, not an empty success, so a client cannot
// mistake a typo for a child that produced nothing.
func TestRuntimeFailsToGetAnUnknownAgentRun(t *testing.T) {
	fixture := newAgentRunFixture(t)

	got, err := fixture.runtime.GetSessionAgentRun(
		context.Background(),
		fixture.sessionID,
		uuid.New(),
	)
	require.Error(t, err)
	assert.Nil(t, got)
}

// The event replay comes from SQLite and carries the run's current state, so a
// client that reconnects mid-run sees both the history and where it stands.
func TestRuntimeReplaysAgentRunEventsFromSQLite(t *testing.T) {
	ctx := context.Background()
	fixture := newAgentRunFixture(t)
	run := fixture.startRun(t, "replay", `[]`)

	for _, event := range []session.AgentRunEventInput{
		{EventType: "agent.started", PayloadJSON: `{"step":1}`},
		{EventType: "agent.tool_call", PayloadJSON: `{"step":2}`},
		{EventType: "agent.completed", PayloadJSON: `{"step":3}`},
	} {
		_, appendErr := fixture.store.AppendAgentRunEvent(
			ctx,
			fixture.sessionID,
			run.ID,
			event,
		)
		require.NoError(t, appendErr)
	}

	page, err := fixture.runtime.ListSessionAgentRunEvents(
		ctx,
		fixture.sessionID,
		run.ID,
		api.ListSessionAgentRunEventsParams{},
	)
	require.NoError(t, err)
	require.Len(t, page.Events, 3)

	assert.Equal(t, run.ID, page.AgentRunId)
	assert.Equal(
		t,
		api.AgentRunEventPageState(models.AgentRunStateRunning),
		page.State,
	)
	assert.Equal(t, "agent.started", page.Events[0].Type)
	require.NotNil(t, page.Events[0].Payload)
	assert.Equal(t, float64(1), (*page.Events[0].Payload)["step"])

	limit := int32(2)
	firstPage, err := fixture.runtime.ListSessionAgentRunEvents(
		ctx,
		fixture.sessionID,
		run.ID,
		api.ListSessionAgentRunEventsParams{Limit: &limit},
	)
	require.NoError(t, err)
	require.Len(t, firstPage.Events, 2)
	assert.True(t, firstPage.HasMore)

	rest, err := fixture.runtime.ListSessionAgentRunEvents(
		ctx,
		fixture.sessionID,
		run.ID,
		api.ListSessionAgentRunEventsParams{Cursor: &firstPage.NextCursor},
	)
	require.NoError(t, err)
	require.Len(t, rest.Events, 1)
	assert.Equal(t, "agent.completed", rest.Events[0].Type)
	assert.False(t, rest.HasMore)
}

// Cancelling a live child records the request durably. Cancelling one that
// already finished reports its state instead of failing, which is what keeps
// a double click from turning into an error.
func TestRuntimeCancelsAnAgentRunAndReportsAnAlreadyFinishedOne(t *testing.T) {
	ctx := context.Background()
	fixture := newAgentRunFixture(t)

	running := fixture.startRun(t, "running", `[]`)

	cancelled, err := fixture.runtime.CancelSessionAgentRun(
		ctx,
		fixture.sessionID,
		running.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, cancelled)
	assert.Equal(t, running.ID, cancelled.AgentRunId)
	assert.True(t, cancelled.CancelRequested)

	stored, err := fixture.store.GetAgentRun(ctx, fixture.sessionID, running.ID)
	require.NoError(t, err)
	assert.True(
		t,
		stored.CancelRequested,
		"the cancellation must be durable, not only in memory",
	)

	finished := fixture.startRun(t, "finished", `[]`)
	_, err = fixture.store.FinalizeAgentRun(
		ctx,
		fixture.sessionID,
		finished.ID,
		session.FinalizeAgentRunInput{
			State:        models.AgentRunStateCompleted,
			FinishReason: "stop",
		},
	)
	require.NoError(t, err)

	repeat, err := fixture.runtime.CancelSessionAgentRun(
		ctx,
		fixture.sessionID,
		finished.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, repeat)
	assert.False(
		t,
		repeat.CancelRequested,
		"a completed child has nothing left to cancel",
	)
	assert.Equal(
		t,
		api.AgentRunCancelResponseState(models.AgentRunStateCompleted),
		repeat.State,
	)
}

// A malformed page request is rejected before it reaches SQLite.
func TestRuntimeRejectsAnOutOfRangeAgentRunPageRequest(t *testing.T) {
	fixture := newAgentRunFixture(t)
	negative := int32(-1)

	page, err := fixture.runtime.ListSessionAgentRuns(
		context.Background(),
		fixture.sessionID,
		api.ListSessionAgentRunsParams{Limit: &negative},
	)
	require.Error(t, err)
	assert.Nil(t, page)
}

// A stored row whose JSON columns are corrupt must surface as an error rather
// than a half-built response, because the payload columns are written by the
// worker and a bad write would otherwise reach the client as valid data.
func TestAgentRunModelToAPIRejectsCorruptPayloadColumns(t *testing.T) {
	t.Parallel()

	converted, err := agentRunModelToAPI(nil)
	require.Error(t, err)
	assert.Empty(t, converted.AgentRunId)

	_, err = agentRunModelToAPI(&models.AgentRun{
		AllowedToolsJSON:     "{not json",
		ResponseMessagesJSON: `[]`,
	})
	require.Error(t, err)

	_, err = agentRunModelToAPI(&models.AgentRun{
		AllowedToolsJSON:     `[]`,
		ResponseMessagesJSON: "{not json",
	})
	require.Error(t, err)
}

// A corrupt event payload is an error for the same reason.
func TestAgentRunEventToAPIRejectsACorruptPayload(t *testing.T) {
	t.Parallel()

	_, err := agentRunEventToAPI(&models.AgentRunEvent{
		EventType:   "agent.started",
		PayloadJSON: "{not json",
	})
	require.Error(t, err)
}

// A row with no payload converts to a nil payload rather than an empty object,
// so a client can tell "no payload" from "{}". The store rejects an empty
// payload column, so only a row written outside it reaches this branch.
func TestAgentRunEventToAPIKeepsAnAbsentPayloadNil(t *testing.T) {
	t.Parallel()

	converted, err := agentRunEventToAPI(&models.AgentRunEvent{
		Sequence:  7,
		EventType: "agent.completed",
	})
	require.NoError(t, err)
	assert.Equal(t, "agent.completed", converted.Type)
	assert.Equal(t, int64(7), converted.Sequence)
	assert.Nil(t, converted.Payload)
}
