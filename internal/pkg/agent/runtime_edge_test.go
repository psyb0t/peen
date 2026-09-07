package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errRuntimeEdgeProviderFailed = errors.New("provider failed")
	errRuntimeEdgeGenericFailure = errors.New("failed")
	errRuntimeEdgeSinkFailed     = errors.New("sink failed")
)

func TestNewRuntimeValidatesDependenciesAndOptions(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())

	base := RuntimeOptions{
		Store:            fixture.runtime.store,
		Resolver:         fixture.runtime.resolver,
		Models:           fixture.runtime.models,
		RootAgent:        fixture.runtime.rootAgent,
		DefaultModel:     fixture.runtime.defaultModel,
		DefaultWorkspace: fixture.runtime.defaultWorkspace,
		MaxContextTokens: fixture.runtime.maxContextTokens,
		TurnTimeout:      fixture.runtime.turnTimeout,
	}
	testCases := []struct {
		name   string
		mutate func(*RuntimeOptions)
		want   error
	}{
		{
			name: "missing store",
			mutate: func(options *RuntimeOptions) {
				options.Store = nil
			},
			want: commerr.ErrRequiredFieldNotSet,
		},
		{
			name: "missing resolver",
			mutate: func(options *RuntimeOptions) {
				options.Resolver = nil
			},
			want: commerr.ErrRequiredFieldNotSet,
		},
		{
			name: "missing model resolver",
			mutate: func(options *RuntimeOptions) {
				options.Models = nil
			},
			want: commerr.ErrRequiredFieldNotSet,
		},
		{
			name: "invalid execution settings",
			mutate: func(options *RuntimeOptions) {
				options.RootAgent = ""
				options.DefaultModel = ""
				options.DefaultWorkspace = ""
				options.MaxContextTokens = 0
				options.TurnTimeout = 0
			},
			want: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			options := base
			tc.mutate(&options)

			runtime, err := NewRuntime(options)

			assert.Nil(t, runtime)
			require.ErrorIs(t, err, tc.want)
		})
	}

	runtime, err := NewRuntime(base)
	require.NoError(t, err)
	assert.Equal(t, defaultSystemPrompt, runtime.baseSystemPrompt)
}

func TestRuntimeResolveInput(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())

	testCases := []struct {
		name          string
		input         TurnRequest
		wantWorkspace string
		wantModel     string
		wantErr       error
	}{
		{
			name:          "uses runtime defaults",
			input:         TurnRequest{Message: "inspect"},
			wantWorkspace: fixture.workspace,
			wantModel:     runtimeTestModelReference,
		},
		{
			name: "accepts explicit settings",
			input: TurnRequest{
				Message:          "inspect",
				Workspace:        fixture.otherWorkspace,
				Model:            "other/model",
				SystemPrompt:     "extra",
				SystemPromptMode: PromptModeAppend,
			},
			wantWorkspace: fixture.otherWorkspace,
			wantModel:     "other/model",
		},
		{
			name:    "rejects blank message",
			input:   TurnRequest{Message: " \t"},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "rejects prompt mode without prompt",
			input: TurnRequest{
				Message:          "inspect",
				SystemPromptMode: PromptModeAppend,
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "rejects unknown prompt mode",
			input: TurnRequest{
				Message:          "inspect",
				SystemPrompt:     "extra",
				SystemPromptMode: PromptMode("unknown"),
			},
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			workspace, model, err := fixture.runtime.resolveInput(tc.input)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantWorkspace, workspace)
			assert.Equal(t, tc.wantModel, model)
		})
	}
}

func TestRuntimeFailurePersistsTerminalState(t *testing.T) {
	failure := errRuntimeEdgeProviderFailed
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Turn{Err: failure},
	))

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "inspect",
		Workspace: fixture.workspace,
	})

	assert.Nil(t, result)
	require.ErrorIs(t, err, failure)

	query := repositories.Use(fixture.handle.GormDB)
	turn, err := query.Turn.WithContext(context.Background()).First()
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateFailed, turn.State)
	messages, err := fixture.store.ListMessages(
		context.Background(),
		turn.SessionID,
		session.ListMessagesOptions{Limit: session.DefaultPageLimit},
	)
	require.NoError(t, err)
	require.Len(t, messages.Items, 1)
	assert.False(t, messages.Items[0].Incomplete)
	events, err := query.Event.WithContext(context.Background()).
		Where(query.Event.TurnID.Eq(turn.ID)).Find()
	require.NoError(t, err)
	assert.Equal(t, EventTypeTurnFailed, events[len(events)-1].EventType)
}

func TestPromptHistoryAndRuntimeHelpers(t *testing.T) {
	history := &session.History{
		Compaction: &models.Compaction{Summary: "earlier conversation"},
		Messages: []*models.Message{
			{
				Role:          models.MessageRoleUser,
				Content:       "question",
				ToolCallsJSON: "[]",
			},
			{
				Role:          models.MessageRoleAssistant,
				Content:       "answer",
				Thinking:      "reasoning",
				ToolCallsJSON: `[{"id":"call-1","name":"read_file","arguments":"{}"}]`,
			},
			{
				Role:          models.MessageRoleTool,
				Content:       "tool result",
				ToolCallID:    "call-1",
				IsError:       true,
				ToolCallsJSON: "[]",
			},
		},
	}

	prompt, plan, err := promptFromHistory("base prompt", history)
	require.NoError(t, err)
	assert.Equal(t, "base prompt", prompt.SystemMessage())
	assert.NotContains(
		t,
		prompt.SystemMessage(),
		"earlier conversation",
		"a summary must never be promoted to system authority",
	)

	messages := prompt.Messages()
	require.Len(t, messages, 5)
	assert.Equal(t, elelem.RoleUser, messages[1].Role)
	assert.Contains(t, messages[1].Text(), "earlier conversation")
	assert.Contains(t, messages[1].Text(), compactionSummaryLead)
	assert.Equal(t, elelem.RoleTool, messages[4].Role)
	assert.True(t, messages[4].ToolResultIsError)

	// The system message and the synthetic summary lead. The assistant
	// message and its tool result stay in one unit.
	assert.Equal(t, 2, plan.LeadCount)
	assert.True(t, plan.HasSummary)
	require.Len(t, plan.Units, 2)
	assert.Equal(t, 1, plan.Units[0].MessageCount)
	assert.Equal(t, 2, plan.Units[1].MessageCount)
	require.NoError(t, plan.verify(messages))

	testCases := []struct {
		name    string
		message *models.Message
		wantErr error
	}{
		{
			name:    "nil history message",
			wantErr: commerr.ErrInvalidState,
		},
		{
			name: "unknown role",
			message: &models.Message{
				Role:          models.MessageRole("unknown"),
				ToolCallsJSON: "[]",
			},
			wantErr: commerr.ErrInvalidState,
		},
		{
			name: "invalid tool calls JSON",
			message: &models.Message{
				Role:          models.MessageRoleUser,
				ToolCallsJSON: "not JSON",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, convertErr := elelemMessage(tc.message)
			require.Error(t, convertErr)
			if tc.wantErr != nil {
				require.ErrorIs(t, convertErr, tc.wantErr)
			}
		})
	}

	state, classification, eventType := failedTurnState(context.Canceled)
	assert.Equal(t, models.TurnStateCancelled, state)
	assert.Equal(t, failureClassCancelled, classification)
	assert.Equal(t, EventTypeTurnCancelled, eventType)
	state, classification, eventType = failedTurnState(errRuntimeEdgeGenericFailure)
	assert.Equal(t, models.TurnStateFailed, state)
	assert.Equal(t, failureClassAgentRun, classification)
	assert.Equal(t, EventTypeTurnFailed, eventType)

	turn := runtimeTurn{requestID: uuid.New()}
	require.NoError(t, turn.emit(EventTypeTextDelta, textDeltaPayload{Text: "text"}))

	assert.Len(t, turn.pendingTranscript().events, 1)
	assert.True(t, markIncomplete([]session.MessageInput{{Content: "partial"}})[0].Incomplete)

	sinkFailure := errRuntimeEdgeSinkFailed
	turn.sink = func(Event) error { return sinkFailure }
	require.ErrorIs(
		t,
		turn.emit(EventTypeTextDelta, textDeltaPayload{Text: "text"}),
		sinkFailure,
	)
	require.Error(t, turn.emit(EventTypeTextDelta, make(chan int)))
}
