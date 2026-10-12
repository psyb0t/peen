package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplayDirectoryRequiresSuccessfulPairedCalls(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		role      models.MessageRole
		tool      string
		isError   bool
		wrongTurn bool
		want      bool
	}{
		{name: "successful", role: models.MessageRoleTool, tool: toolNameChangeDirectory, want: true},
		{name: "user spoof", role: models.MessageRoleUser, tool: toolNameChangeDirectory},
		{name: "other tool", role: models.MessageRoleTool, tool: toolNameReadFile},
		{name: "failed hop", role: models.MessageRoleTool, tool: toolNameChangeDirectory, isError: true},
		{name: "other turn", role: models.MessageRoleTool, tool: toolNameChangeDirectory, wrongTurn: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			turnID := uuid.New()
			resultTurnID := turnID
			if tc.wrongTurn {
				resultTurnID = uuid.New()
			}
			pending := map[directoryResultKey]directoryResult{}
			_, found := replayDirectory(&models.Message{
				TurnID: resultTurnID, Role: tc.role, IsError: tc.isError,
				ToolCallID: "hop", Workspace: "nested", Sequence: 2,
				Content: "truncated output",
			}, pending)
			assert.False(t, found)
			directory, found := replayDirectory(&models.Message{
				TurnID: turnID, Role: models.MessageRoleAssistant, Sequence: 1,
				ToolCallsJSON: directoryToolCalls(t, elelem.ToolCall{ID: "hop", Name: tc.tool}),
			}, pending)
			assert.Equal(t, tc.want, found)
			if tc.want {
				assert.Equal(t, "nested", directory)
			}
		})
	}
}

func TestCurrentDirectoryWalksPagesIncludingCompactedRows(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	ctx := context.Background()
	result, err := fixture.runtime.Run(ctx, TurnRequest{Message: "initial"})
	require.NoError(t, err)
	query := repositories.Use(fixture.handle.GormDB)
	turn, err := query.Turn.WithContext(ctx).Where(query.Turn.SessionID.Eq(result.SessionID)).First()
	require.NoError(t, err)
	compactionID := uuid.New()
	previous, err := query.Message.WithContext(ctx).Where(query.Message.SessionID.Eq(result.SessionID)).Order(query.Message.Sequence).Find()
	require.NoError(t, err)
	require.Len(t, previous, 2)
	require.NoError(t, query.Compaction.WithContext(ctx).Create(&models.Compaction{
		ID: compactionID, SessionID: result.SessionID,
		FromMessageID: previous[0].ID, ToMessageID: previous[1].ID,
		FromSequence: 1, ToSequence: 2, SourceMessageCount: 2,
		Summary: "summary", ModelID: runtimeTestModelID,
	}))
	messages := []*models.Message{
		{
			ID: uuid.New(), SessionID: result.SessionID, TurnID: turn.ID, Sequence: 3,
			Role: models.MessageRoleAssistant, CompactionID: &compactionID,
			ToolCallsJSON: directoryToolCalls(t, elelem.ToolCall{ID: "hop", Name: toolNameChangeDirectory}),
		},
		{
			ID: uuid.New(), SessionID: result.SessionID, TurnID: turn.ID, Sequence: 4,
			Role: models.MessageRoleTool, Workspace: "nested", ToolCallID: "hop", CompactionID: &compactionID, ToolCallsJSON: "[]",
		},
	}
	// Put the result at the end of one page and its call on the next.
	for sequence := int64(5); sequence <= int64(directoryReplayPageSize+3); sequence++ {
		messages = append(messages, &models.Message{
			ID: uuid.New(), SessionID: result.SessionID, TurnID: turn.ID,
			Sequence: sequence, Role: models.MessageRoleUser, Content: "padding", ToolCallsJSON: "[]",
		})
	}
	require.NoError(t, query.Message.WithContext(ctx).Create(messages...))
	directory, err := fixture.runtime.currentDirectory(ctx, result.SessionID, fixture.workspace)
	require.NoError(t, err)
	assert.Equal(t, "nested", directory)
}
