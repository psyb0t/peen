package agent

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childBootstrapTestRun() *models.AgentRun {
	parentID := uuid.New()

	return &models.AgentRun{
		ID: uuid.New(), SessionID: uuid.New(), ParentTurnID: uuid.New(),
		RequestID: uuid.New(), ParentAgentRunID: &parentID,
		ParentToolCallID: "example-call", Name: "exampleChild",
		Definition: models.AgentRunDefinitionAdHoc, Depth: 2,
		Workspace: "/example", ModelReference: "example/model", ModelID: "model",
		Task: "inspect example files", Instructions: "read the example sources",
		AllowedToolsJSON: `["readFile","searchText"]`, SystemPrompt: "child prompt",
	}
}

func TestChildBootstrapRetainsLaunchAndBudgetAcrossJSON(t *testing.T) {
	t.Parallel()
	run := childBootstrapTestRun()
	saved, err := snapshotChildWorkflowBootstrap(run, 8, 3)
	require.NoError(t, err)
	require.NotNil(t, saved)
	encoded, err := json.Marshal(saved)
	require.NoError(t, err)
	var restored workflowChildBootstrap
	require.NoError(t, json.Unmarshal(encoded, &restored))
	assert.Equal(t, *saved, restored)
	assert.Equal(t, run.ID, restored.AgentRunID)
	assert.Equal(t, run.ParentAgentRunID, restored.ParentAgentRunID)
	assert.Equal(t, run.Instructions, restored.Instructions)
	assert.Equal(t, []string{"readFile", "searchText"}, restored.AllowedTools)
	assert.Equal(t, 8, restored.MaxRounds)
	assert.Equal(t, 3, restored.CompletedRounds)
	definition := restored.definition()
	definition.allowedTools[0] = "changedTool"
	assert.Equal(t, "readFile", restored.AllowedTools[0])
	assert.Equal(t, run.Name, definition.name)
	assert.Equal(t, run.Instructions, definition.instructions)
	*saved.ParentAgentRunID = uuid.New()
	assert.NotEqual(t, run.ParentAgentRunID, saved.ParentAgentRunID)
	assert.Equal(t, run.ParentAgentRunID, restored.ParentAgentRunID)
}

func TestChildBootstrapRejectsMalformedState(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*workflowChildBootstrap)
	}{
		{name: "version", change: func(b *workflowChildBootstrap) { b.Version++ }},
		{name: "session", change: func(b *workflowChildBootstrap) {
			b.SessionID = uuid.Nil
		}},
		{name: "run", change: func(b *workflowChildBootstrap) {
			b.AgentRunID = uuid.Nil
		}},
		{name: "turn", change: func(b *workflowChildBootstrap) {
			b.TurnID = uuid.Nil
		}},
		{name: "request", change: func(b *workflowChildBootstrap) {
			b.RequestID = uuid.Nil
		}},
		{name: "depth", change: func(b *workflowChildBootstrap) { b.Depth = 0 }},
		{name: "parent", change: func(b *workflowChildBootstrap) {
			id := uuid.Nil
			b.ParentAgentRunID = &id
		}},
		{name: "self parent", change: func(b *workflowChildBootstrap) {
			b.ParentAgentRunID = &b.AgentRunID
		}},
		{name: "maximum", change: func(b *workflowChildBootstrap) {
			b.MaxRounds = 0
		}},
		{name: "negative used", change: func(b *workflowChildBootstrap) {
			b.CompletedRounds = -1
		}},
		{name: "exhausted", change: func(b *workflowChildBootstrap) {
			b.CompletedRounds = b.MaxRounds
		}},
		{name: "definition", change: func(b *workflowChildBootstrap) {
			b.Definition = "unknown"
		}},
		{name: "instructions", change: func(b *workflowChildBootstrap) {
			b.Instructions = " "
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := snapshotChildWorkflowBootstrap(childBootstrapTestRun(), 8, 3)
			require.NoError(t, err)
			tc.change(b)
			require.ErrorIs(t, b.validate(), commerr.ErrValidationFailed)
		})
	}
}

func TestChildBootstrapRejectsChangedLaunch(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*models.AgentRun)
	}{
		{name: "session", change: func(run *models.AgentRun) {
			run.SessionID = uuid.New()
		}},
		{name: "run", change: func(run *models.AgentRun) { run.ID = uuid.New() }},
		{name: "turn", change: func(run *models.AgentRun) {
			run.ParentTurnID = uuid.New()
		}},
		{name: "request", change: func(run *models.AgentRun) {
			run.RequestID = uuid.New()
		}},
		{name: "parent", change: func(run *models.AgentRun) {
			run.ParentAgentRunID = nil
		}},
		{name: "parent call", change: func(run *models.AgentRun) {
			run.ParentToolCallID = "another-call"
		}},
		{name: "depth", change: func(run *models.AgentRun) { run.Depth++ }},
		{name: "name", change: func(run *models.AgentRun) { run.Name = "another" }},
		{name: "task", change: func(run *models.AgentRun) { run.Task = "another" }},
		{name: "instructions", change: func(run *models.AgentRun) {
			run.Instructions = "another"
		}},
		{name: "definition", change: func(run *models.AgentRun) {
			run.Definition = models.AgentRunDefinitionStored
		}},
		{name: "model", change: func(run *models.AgentRun) {
			run.ModelReference = "another/model"
		}},
		{name: "model id", change: func(run *models.AgentRun) {
			run.ModelID = "another"
		}},
		{name: "tools", change: func(run *models.AgentRun) {
			run.AllowedToolsJSON = `["writeFile"]`
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := childBootstrapTestRun()
			b, err := snapshotChildWorkflowBootstrap(run, 8, 3)
			require.NoError(t, err)
			tc.change(run)
			require.ErrorIs(t, b.validateAgainst(run), commerr.ErrConflict)
		})
	}
}

func TestChildBootstrapKeepsOriginalLaunchAcrossDirectoryHop(t *testing.T) {
	t.Parallel()
	run := childBootstrapTestRun()
	b, err := snapshotChildWorkflowBootstrap(run, 8, 3)
	require.NoError(t, err)
	run.Workspace = "/another-example"
	run.SystemPrompt = "current directory instructions"
	require.NoError(t, b.validateAgainst(run))
	assert.Equal(t, "/example", b.Workspace)
	assert.Equal(t, "child prompt", b.SystemPrompt)
}

func TestChildBootstrapRoundBudgetDoesNotReset(t *testing.T) {
	t.Parallel()
	b, err := snapshotChildWorkflowBootstrap(childBootstrapTestRun(), 8, 3)
	require.NoError(t, err)
	input := workflowRoundInput{Execution: workflowExecutionInput{
		SessionID: b.SessionID, MaxRounds: 5,
	}, Round: 4}
	require.NoError(t, b.validateRound(input))
	input.Execution.MaxRounds = 8
	require.ErrorIs(t, b.validateRound(input), commerr.ErrValidationFailed)
	input.Execution.MaxRounds = 5
	input.Round = 5
	require.ErrorIs(t, b.validateRound(input), commerr.ErrValidationFailed)
	input.Round = 0
	input.Execution.SessionID = uuid.New()
	require.ErrorIs(t, b.validateRound(input), commerr.ErrPermissionDenied)
	b.MaxRounds, b.CompletedRounds = math.MaxInt, math.MaxInt-1
	input.Execution.SessionID, input.Execution.MaxRounds = b.SessionID, 1
	require.NoError(t, b.validateRound(input))
}

func TestChildBootstrapRejectsMissingRunAndMalformedTools(t *testing.T) {
	t.Parallel()
	_, err := snapshotChildWorkflowBootstrap(nil, 8, 0)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
	var missing *workflowChildBootstrap
	require.ErrorIs(t, missing.validate(), commerr.ErrRequiredFieldNotSet)
	run := childBootstrapTestRun()
	run.AllowedToolsJSON = "{"
	_, err = snapshotChildWorkflowBootstrap(run, 8, 0)
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)
}

func TestChildBootstrapRejectsModelInputBeforeResolution(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*workflowConversation, *workflowRoundInput)
		wantErr error
	}{
		{
			name: "version", wantErr: commerr.ErrValidationFailed,
			change: func(c *workflowConversation, _ *workflowRoundInput) {
				c.Child.Version++
			},
		},
		{
			name: "root", wantErr: commerr.ErrConflict,
			change: func(c *workflowConversation, _ *workflowRoundInput) {
				c.Root = &workflowRootBootstrap{}
			},
		},
		{
			name: "model", wantErr: commerr.ErrConflict,
			change: func(c *workflowConversation, _ *workflowRoundInput) {
				c.ModelReference = "other/model"
			},
		},
		{
			name: "reset budget", wantErr: commerr.ErrValidationFailed,
			change: func(_ *workflowConversation, input *workflowRoundInput) {
				input.Execution.MaxRounds = 8
			},
		},
		{
			name: "exhausted", wantErr: commerr.ErrValidationFailed,
			change: func(_ *workflowConversation, input *workflowRoundInput) {
				input.Round = 5
			},
		},
		{
			name: "session", wantErr: commerr.ErrPermissionDenied,
			change: func(_ *workflowConversation, input *workflowRoundInput) {
				input.Execution.SessionID = uuid.New()
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := snapshotChildWorkflowBootstrap(childBootstrapTestRun(), 8, 3)
			require.NoError(t, err)
			c := workflowConversation{Child: b, ModelReference: b.ModelReference}
			input := workflowRoundInput{Execution: workflowExecutionInput{
				SessionID: b.SessionID, MaxRounds: 5,
			}}
			tc.change(&c, &input)
			input.Execution.Context, err = json.Marshal(c)
			require.NoError(t, err)
			_, _, err = workflowModelRequest(nil, input)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestChildBootstrapSurvivesSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture, run, _ := nativeChildCompactionFixture(t)
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	execution, err := store.GetExecution(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal([]byte(execution.ContextJSON),
		&conversation))
	require.NotNil(t, conversation.Child)
	require.NoError(t, conversation.Child.validateAgainst(run))
	assert.Equal(t, run.Instructions, conversation.Child.Instructions)
	assert.Equal(t, run.ID, conversation.Child.AgentRunID)
	assert.Equal(t, fixture.input.MaxRounds, conversation.Child.MaxRounds)
}

func TestChildBootstrapRejectsMismatchedModelMetadata(t *testing.T) {
	t.Parallel()
	saved, err := snapshotChildWorkflowBootstrap(childBootstrapTestRun(), 8, 0)
	require.NoError(t, err)
	saved.Model = &elelem.Model{ID: "another-model"}
	require.ErrorIs(t, saved.validate(), commerr.ErrConflict)
}
