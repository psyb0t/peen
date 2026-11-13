package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recoveryCountingStore struct {
	session.Storage
	rows    []*models.Message
	offsets []int
}

func (s *recoveryCountingStore) ListMessages(
	_ context.Context,
	_ uuid.UUID,
	options session.ListMessagesOptions,
) (*session.MessagePage, error) {
	s.offsets = append(s.offsets, options.Offset)
	start := min(options.Offset, len(s.rows))
	end := min(start+options.Limit, len(s.rows))

	return &session.MessagePage{
		Items: s.rows[start:end], HasMore: end < len(s.rows),
	}, nil
}

func (s *recoveryCountingStore) ListAgentRunMessages(
	ctx context.Context,
	sessionID, _ uuid.UUID,
	options session.ListAgentRunMessagesOptions,
) (*session.AgentRunMessagePage, error) {
	page, err := s.ListMessages(ctx, sessionID, session.ListMessagesOptions{
		Limit: options.Limit, Offset: options.Offset,
	})
	if err != nil {
		return nil, err
	}
	var rows []*models.AgentRunMessage
	for _, row := range page.Items {
		rows = append(rows, &models.AgentRunMessage{
			Role: row.Role, Content: row.Content, ToolCallsJSON: row.ToolCallsJSON,
			ToolCallID: row.ToolCallID, IsError: row.IsError,
			Incomplete: row.Incomplete,
		})
	}

	return &session.AgentRunMessagePage{
		Items: rows, HasMore: page.HasMore,
	}, nil
}

func TestFileRecoveryActorIsolation(t *testing.T) {
	t.Parallel()
	store := &recoveryCountingStore{rows: []*models.Message{{
		Role: models.MessageRoleUser, Content: "synthetic",
	}}}
	runtime := &Runtime{store: store}
	input := workflowRoundInput{Execution: workflowExecutionInput{
		SessionID: uuid.New(),
	}}
	for _, conversation := range []workflowConversation{
		{},
		{Child: &workflowChildBootstrap{AgentRunID: uuid.New()}},
		{Child: &workflowChildBootstrap{AgentRunID: uuid.New()}},
	} {
		store.offsets = nil
		_, err := runtime.workflowFileRecovery(t.Context(), conversation, input)
		require.NoError(t, err)
		assert.Equal(t, []int{0}, store.offsets)
	}
	assert.Len(t, runtime.fileRecoveryActors, 3)
}

func TestFileRecoveryIncrementalAndShrink(t *testing.T) {
	t.Parallel()
	calls, err := json.Marshal([]elelem.ToolCall{{
		ID: "read", Name: toolNameReadFile,
		Arguments: json.RawMessage(`{}`),
	}})
	require.NoError(t, err)
	store := &recoveryCountingStore{}
	for range fileRecoveryPageSize - 1 {
		store.rows = append(store.rows, &models.Message{
			Role: models.MessageRoleUser, Content: "synthetic",
		})
	}
	store.rows = append(store.rows, &models.Message{
		Role: models.MessageRoleAssistant, ToolCallsJSON: string(calls),
	})
	runtime := &Runtime{store: store}
	input := workflowRoundInput{Execution: workflowExecutionInput{
		SessionID: uuid.New(),
	}}
	first, err := runtime.workflowFileRecovery(t.Context(),
		workflowConversation{}, input)
	require.NoError(t, err)
	assert.Empty(t, first.reads)
	assert.Equal(t, []int{0}, store.offsets)
	store.offsets = nil
	store.rows = append(store.rows, &models.Message{
		Role: models.MessageRoleTool, ToolCallID: "read",
		Content: recoveryTestText(t, recoveryTestOutput("/new", "text")),
	})
	second, err := runtime.workflowFileRecovery(t.Context(),
		workflowConversation{}, input)
	require.NoError(t, err)
	assert.Equal(t, []int{fileRecoveryPageSize}, store.offsets)
	require.Len(t, second.reads, 1)
	assert.Empty(t, first.reads)
	cold := &Runtime{store: store}
	rebuilt, err := cold.workflowFileRecovery(t.Context(),
		workflowConversation{}, input)
	require.NoError(t, err)
	assert.Equal(t, rebuilt.reads, second.reads)
	assert.Equal(t, rebuilt.readCalls, second.readCalls)
	store.rows = slices.Clone(store.rows[:1])
	store.offsets = nil
	shrunk, err := runtime.workflowFileRecovery(t.Context(),
		workflowConversation{}, input)
	require.NoError(t, err)
	assert.Empty(t, shrunk.reads)
	assert.Equal(t, []int{fileRecoveryPageSize + 1, fileRecoveryPageSize, 0},
		store.offsets)
}

func TestFileRecoveryActorCacheBounded(t *testing.T) {
	t.Parallel()
	runtime := &Runtime{store: &recoveryCountingStore{}}
	inputs := make([]workflowRoundInput, 0, fileRecoveryMaxActors)
	for range fileRecoveryMaxActors {
		input := workflowRoundInput{Execution: workflowExecutionInput{
			SessionID: uuid.New(),
		}}
		inputs = append(inputs, input)
		_, err := runtime.workflowFileRecovery(t.Context(),
			workflowConversation{}, input)
		require.NoError(t, err)
	}
	_, err := runtime.workflowFileRecovery(t.Context(),
		workflowConversation{}, inputs[0])
	require.NoError(t, err)
	_, err = runtime.workflowFileRecovery(t.Context(),
		workflowConversation{}, workflowRoundInput{Execution: workflowExecutionInput{
			SessionID: uuid.New(),
		}})
	require.NoError(t, err)
	assert.Len(t, runtime.fileRecoveryActors, fileRecoveryMaxActors)
	assert.Contains(t, runtime.fileRecoveryActors,
		fileRecoveryActor{sessionID: inputs[0].Execution.SessionID})
	assert.NotContains(t, runtime.fileRecoveryActors,
		fileRecoveryActor{sessionID: inputs[1].Execution.SessionID})
}

func TestFileRecoveryConcurrentRequests(t *testing.T) {
	t.Parallel()
	runtime := &Runtime{store: &recoveryCountingStore{}}
	count := fileRecoveryMaxActors + 1
	results := make(chan error, count)
	for range count {
		go func() {
			_, err := runtime.workflowFileRecovery(t.Context(),
				workflowConversation{}, workflowRoundInput{
					Execution: workflowExecutionInput{SessionID: uuid.New()},
				})
			results <- err
		}()
	}
	for range count {
		require.NoError(t, <-results)
	}
	assert.Len(t, runtime.fileRecoveryActors, fileRecoveryMaxActors)
}

func TestFileRecoveryTerminalObserver(t *testing.T) {
	t.Parallel()
	for _, eventType := range []string{
		EventTypeAgentRunCompleted, EventTypeAgentRunFailed,
		EventTypeAgentRunCancelled,
	} {
		t.Run(eventType, func(t *testing.T) {
			t.Parallel()
			sessionID, childID := uuid.New(), uuid.New()
			root := fileRecoveryActor{sessionID: sessionID}
			child := fileRecoveryActor{sessionID: sessionID, agentRunID: childID}
			other := fileRecoveryActor{sessionID: uuid.New()}
			runtime := &Runtime{fileRecoveryActors: map[fileRecoveryActor]*fileRecoveryCache{
				root: {}, child: {}, other: {},
			}}
			forwarded := 0
			observe := runtime.fileRecoveryObserver(t.Context(),
				func(id uuid.UUID, _ Event) {
					assert.Equal(t, sessionID, id)
					forwarded++
				})
			payload, err := json.Marshal(agentRunLivePayload{AgentRunID: childID})
			require.NoError(t, err)
			observe(sessionID, Event{Type: eventType, Payload: payload})
			assert.NotContains(t, runtime.fileRecoveryActors, child)
			assert.Contains(t, runtime.fileRecoveryActors, root)
			assert.Contains(t, runtime.fileRecoveryActors, other)
			observe(sessionID, Event{Type: EventTypeTurnFailed})
			assert.NotContains(t, runtime.fileRecoveryActors, root)
			assert.Contains(t, runtime.fileRecoveryActors, other)
			assert.Equal(t, 2, forwarded)
		})
	}
}

func TestFileRecoveryRootTranscriptStaysUnchanged(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	source := filepath.Join(fixture.workspace, "source.txt")
	writeRuntimeFile(t, source, "synthetic source\n")
	executor, err := tools.NewExecutor(tools.Options{Workspace: fixture.workspace})
	require.NoError(t, err)
	output, err := executor.ReadFile(t.Context(), tools.ReadFileInput{Path: source})
	require.NoError(t, err)
	lease, err := fixture.store.AcquireTurn(t.Context(),
		fixture.runtime.sessionID, session.StartTurnInput{
			RequestID: uuid.New(), Workspace: fixture.workspace,
		})
	require.NoError(t, err)
	text := recoveryTestText(t, output)
	calls, err := json.Marshal([]elelem.ToolCall{{
		ID:   "read",
		Name: toolNameReadFile, Arguments: json.RawMessage(`{}`),
	}})
	require.NoError(t, err)
	require.NoError(t, fixture.store.AppendCheckpoint(t.Context(), lease,
		[]session.MessageInput{
			{Role: models.MessageRoleAssistant, ToolCallsJSON: string(calls)},
			{Role: models.MessageRoleTool, ToolCallID: "read", Content: text},
		}, nil))
	before, err := fixture.store.ListMessages(t.Context(), lease.SessionID,
		session.ListMessagesOptions{Limit: fileRecoveryPageSize})
	require.NoError(t, err)
	conversation := workflowConversation{
		ModelReference: runtimeTestModelReference, SystemPrompt: "system",
		Root: &workflowRootBootstrap{},
		Runtime: &workflowRuntimeState{Directory: directoryMarker{
			Directory: fixture.workspace,
		}},
		Messages: []elelem.Message{compactionSummaryMessage(
			compactionSummaryLead + "previous work")}, MaxContextTokens: 10000,
	}
	request := elelem.NewRequest(elelem.New(driver)).
		WithModel(elelem.Model{ID: runtimeTestModelID}).
		WithMaxContextTokens(10000).WithOutputReserveTokens(0)
	require.NoError(t, fixture.runtime.recoverWorkflowFiles(t.Context(),
		request, conversation, workflowRoundInput{Execution: workflowExecutionInput{
			SessionID: lease.SessionID,
		}}))
	_, err = request.Run(t.Context())
	require.NoError(t, err)
	sent, ok := driver.LastRequest()
	require.True(t, ok)
	entries := recoveryTestEntries(t, sent.Messages)
	require.Len(t, entries, 1)
	assert.True(t, entries[0].Full)
	attachments := 0
	for _, message := range sent.Messages {
		if message.Role == elelem.RoleUser && isFileRecoveryMessage(message) {
			attachments++
		}
	}
	assert.Equal(t, 1, attachments)
	after, err := fixture.store.ListMessages(t.Context(), lease.SessionID,
		session.ListMessagesOptions{Limit: fileRecoveryPageSize})
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestFileRecoveryChildUsesItsOwnTranscript(t *testing.T) {
	t.Parallel()
	fixture, input, sink := childModelFixture(t, elelemtest.Text("done"))
	text := recoveryTestText(t, recoveryTestOutput("/missing/child.txt", "text"))
	calls, err := json.Marshal([]elelem.ToolCall{{
		ID:   "childRead",
		Name: toolNameReadFile, Arguments: json.RawMessage(`{}`),
	}})
	require.NoError(t, err)
	_, err = fixture.base.store.AppendAgentRunMessages(t.Context(),
		sink.sessionID, sink.run.ID, []session.AgentRunMessageInput{
			{Role: models.MessageRoleAssistant, ToolCallsJSON: string(calls)},
			{Role: models.MessageRoleTool, ToolCallID: "childRead", Content: text},
		})
	require.NoError(t, err)
	input.Execution = withWorkflowConversation(t, input.Execution,
		func(conversation *workflowConversation) {
			conversation.Messages = []elelem.Message{compactionSummaryMessage(
				compactionTurnSummaryLead + "previous child work")}
			conversation.Compaction = nil
		})
	result, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	model, err := fixture.base.runtime.models.ResolveModel(runtimeTestModelReference)
	require.NoError(t, err)
	driver, ok := model.Client.Driver().(*elelemtest.ScriptedDriver)
	require.True(t, ok)
	sent, ok := driver.LastRequest()
	require.True(t, ok)
	entries := recoveryTestEntries(t, sent.Messages)
	require.Len(t, entries, 1)
	assert.Equal(t, "/missing/child.txt", entries[0].Path)
	assert.False(t, entries[0].Full)
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(result.Output.Context, &saved))
	for _, message := range saved.Messages {
		assert.False(t, isFileRecoveryMessage(message))
	}
	page, err := fixture.base.store.ListAgentRunMessages(t.Context(),
		sink.sessionID, sink.run.ID, session.ListAgentRunMessagesOptions{
			Limit: fileRecoveryPageSize,
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 4)
	assert.Equal(t, text, page.Items[2].Content)
}
