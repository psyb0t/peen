package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type measuredDirectoryStore struct {
	session.Storage
	pageReads  int
	stateReads int
}

func (s *measuredDirectoryStore) GetDirectoryState(ctx context.Context, id uuid.UUID) (*session.DirectoryState, error) {
	s.stateReads++

	return s.Storage.GetDirectoryState(ctx, id)
}

func (s *measuredDirectoryStore) ListMessages(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.ListMessagesOptions,
) (*session.MessagePage, error) {
	s.pageReads++

	return s.Storage.ListMessages(ctx, sessionID, options)
}

func TestDirectoryRecoveryDoesNotPageThroughUnchangedHistory(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		rows  int
		tools bool
	}{
		{name: "short"},
		{name: "long", rows: 1024},
		{name: "long tool history", rows: 1024, tools: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(elelemtest.Text("done")))
			ctx := context.Background()
			result, err := fixture.runtime.Run(ctx, TurnRequest{Message: "initial"})
			require.NoError(t, err)
			query := repositories.Use(fixture.handle.GormDB)
			turn, err := query.Turn.WithContext(ctx).Where(query.Turn.SessionID.Eq(result.SessionID)).First()
			require.NoError(t, err)
			messages := make([]*models.Message, 0, tc.rows)
			for index := range tc.rows {
				message := &models.Message{
					ID: uuid.New(), SessionID: result.SessionID, TurnID: turn.ID,
					Sequence: int64(index + 3), Role: models.MessageRoleUser,
					Content: "unchanged history", ToolCallsJSON: "[]",
				}
				if tc.tools {
					message.Role = models.MessageRoleAssistant
					callID := fmt.Sprintf("call-%d", index/2)
					message.ToolCallsJSON = directoryToolCalls(t, elelem.ToolCall{ID: callID, Name: toolNameReadFile})
					if index%2 == 1 {
						message.Role = models.MessageRoleTool
						message.ToolCallID = callID
						message.Workspace = fixture.workspace
						message.ToolCallsJSON = "[]"
					}
				}
				messages = append(messages, message)
			}
			if len(messages) > 0 {
				require.NoError(t, query.Message.WithContext(ctx).CreateInBatches(messages, directoryReplayPageSize))
			}
			store := &measuredDirectoryStore{Storage: fixture.store}
			fixture.runtime.store = store
			started := time.Now()
			directory, err := fixture.runtime.currentDirectory(ctx, result.SessionID, fixture.workspace)
			require.NoError(t, err)
			t.Logf("rows=%d page_reads=%d duration=%s", tc.rows, store.pageReads, time.Since(started))
			assert.Equal(t, fixture.workspace, directory)
			assert.Zero(t, store.pageReads)
			assert.Equal(t, 1, store.stateReads)
		})
	}
}

func TestCurrentDirectoryIncludesCompactedRows(t *testing.T) {
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
	// Recovery must include old records after a long compacted history.
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
