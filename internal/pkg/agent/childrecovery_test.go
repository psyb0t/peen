package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestChildRecoveryContinuesStoredIdentityAndHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("recovered answer"))
	fixture := newRuntimeFixture(t, driver)
	sessionID := fixture.runtime.SessionID()
	lease, err := fixture.store.AcquireTurn(ctx, sessionID, session.StartTurnInput{RequestID: uuid.New(), Workspace: fixture.workspace})
	require.NoError(t, err)
	run, err := fixture.store.CreateAgentRun(ctx, sessionID, session.StartAgentRunInput{
		ParentTurnID: lease.TurnID, RequestID: uuid.New(), Name: "saved-child",
		Definition: models.AgentRunDefinitionAdHoc, Depth: 1,
		Workspace: fixture.otherWorkspace, ModelReference: runtimeTestModelReference,
		ModelID: runtimeTestModelID, Task: "continue task", Instructions: "saved instructions",
		AllowedToolsJSON: `[]`, SystemPrompt: "saved prompt",
	})
	require.NoError(t, err)
	_, err = fixture.store.AppendAgentRunMessages(ctx, sessionID, run.ID, []session.AgentRunMessageInput{
		{Role: models.MessageRoleUser, Content: "stored task"},
		{Role: models.MessageRoleAssistant, Content: "checkpointed progress"},
	})
	require.NoError(t, err)
	_, err = fixture.store.RecoverInterrupted(ctx)
	require.NoError(t, err)
	_, err = fixture.store.RecoverInterruptedAgentRuns(ctx)
	require.NoError(t, err)
	_, err = fixture.store.RecoverInterruptedModelRuns(ctx)
	require.NoError(t, err)
	restarted, err := NewRuntime(ctx, RuntimeOptions{
		Store: fixture.store, Resolver: fixture.runtime.resolver, Models: fixture.runtime.models,
		RootAgent: runtimeTestAgentName, DefaultModel: runtimeTestModelReference,
		DefaultWorkspace: fixture.workspace, StartupSessionID: sessionID,
		MaxContextTokens: runtimeTestMaxContextTokens, ConfigDirectory: fixture.configDirectory,
		TurnTimeout: runtimeTestTurnTimeout,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restarted.ShutdownAgentRuns(ctx)) })
	require.Eventually(t, func() bool {
		stored, getErr := fixture.store.GetAgentRun(ctx, sessionID, run.ID)

		return getErr == nil && stored.State == models.AgentRunStateCompleted
	}, time.Second, time.Millisecond)
	stored, err := fixture.store.GetAgentRun(ctx, sessionID, run.ID)
	require.NoError(t, err)
	assert.Equal(t, "recovered answer", stored.ResponseText)
	assert.Equal(t, run.RequestID, stored.RequestID)
	assert.Equal(t, run.ParentTurnID, stored.ParentTurnID)
	assert.Equal(t, run.StartedAt, stored.StartedAt)
	requests := driver.Requests()
	require.Len(t, requests, 1)
	assert.Contains(t, requests[0].Messages[0].Text(), "saved instructions")
	assert.Contains(t, requests[0].Messages[0].Text(), runtimeTestOtherRule)
	assert.Contains(t, requests[0].Messages[0].Text(), fixture.otherWorkspace)
	assert.Equal(t, "stored task", requests[0].Messages[1].Text())
	assert.Equal(t, "checkpointed progress", requests[0].Messages[2].Text())
}

func TestChildRecoveryValidatesTheWholeBatchBeforeClaiming(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		configure func(*models.AgentRun, *RuntimeOptions)
	}{
		{name: "unavailable model", configure: func(run *models.AgentRun, _ *RuntimeOptions) { run.ModelReference = "missing/model" }},
		{name: "invalid tool ceiling", configure: func(run *models.AgentRun, _ *RuntimeOptions) { run.AllowedToolsJSON = `{}` }},
		{name: "missing directory", configure: func(run *models.AgentRun, _ *RuntimeOptions) { run.Workspace = filepath.Join(run.Workspace, "missing") }},
		{name: "depth limit", configure: func(run *models.AgentRun, options *RuntimeOptions) { run.Depth = 2; options.AgentLimits.MaxDepth = 1 }},
		{name: "concurrency limit", configure: func(_ *models.AgentRun, options *RuntimeOptions) { options.AgentLimits.MaxConcurrentRuns = 1 }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, first, driver := recoveryFixture(t)
			ctx := context.Background()
			second, err := fixture.store.CreateAgentRun(ctx, first.SessionID, session.StartAgentRunInput{
				ParentTurnID: first.ParentTurnID, RequestID: first.RequestID,
				Name: "second", Definition: models.AgentRunDefinitionAdHoc, Depth: 2,
				Workspace: fixture.otherWorkspace, ModelReference: runtimeTestModelReference,
				ModelID: runtimeTestModelID, Task: "saved task", Instructions: "instructions",
				AllowedToolsJSON: `[]`, SystemPrompt: "saved prompt",
			})
			require.NoError(t, err)
			_, err = fixture.store.RecoverInterruptedAgentRuns(ctx)
			require.NoError(t, err)
			options := RuntimeOptions{
				Store: fixture.store, Resolver: fixture.runtime.resolver, Models: fixture.runtime.models,
				RootAgent: runtimeTestAgentName, DefaultModel: runtimeTestModelReference,
				DefaultWorkspace: fixture.workspace, StartupSessionID: first.SessionID,
				MaxContextTokens: runtimeTestMaxContextTokens, ConfigDirectory: fixture.configDirectory,
				TurnTimeout: runtimeTestTurnTimeout,
			}
			tc.configure(second, &options)
			repository := repositories.Use(fixture.handle.GormDB).AgentRun
			_, err = repository.WithContext(ctx).Where(repository.ID.Eq(second.ID)).UpdateSimple(
				repository.ModelReference.Value(second.ModelReference),
				repository.AllowedToolsJSON.Value(second.AllowedToolsJSON),
				repository.Workspace.Value(second.Workspace), repository.Depth.Value(second.Depth),
			)
			require.NoError(t, err)
			restarted, err := NewRuntime(ctx, options)
			require.Error(t, err)
			assert.Nil(t, restarted)
			assert.Empty(t, driver.Requests())
			for _, run := range []*models.AgentRun{first, second} {
				stored, err := fixture.store.GetAgentRun(ctx, run.SessionID, run.ID)
				require.NoError(t, err)
				assert.Equal(t, models.AgentRunStateInterrupted, stored.State)
			}
		})
	}
}

func recoveryFixture(t *testing.T) (runtimeFixture, *models.AgentRun, *elelemtest.ScriptedDriver) {
	t.Helper()
	ctx := context.Background()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("recovered answer"))
	fixture := newRuntimeFixture(t, driver)
	sessionID := fixture.runtime.SessionID()
	lease, err := fixture.store.AcquireTurn(ctx, sessionID, session.StartTurnInput{RequestID: uuid.New(), Workspace: fixture.workspace})
	require.NoError(t, err)
	run, err := fixture.store.CreateAgentRun(ctx, sessionID, session.StartAgentRunInput{
		ParentTurnID: lease.TurnID, RequestID: uuid.New(), Name: "saved-child",
		Definition: models.AgentRunDefinitionAdHoc, Depth: 1,
		Workspace: fixture.otherWorkspace, ModelReference: runtimeTestModelReference,
		ModelID: runtimeTestModelID, Task: "continue task", Instructions: "saved instructions",
		AllowedToolsJSON: `[]`, SystemPrompt: "saved prompt",
	})
	require.NoError(t, err)

	return fixture, run, driver
}

func restartRecoveryFixture(t *testing.T, fixture runtimeFixture) *Runtime {
	t.Helper()

	return restartRecoveryFixtureWithOptions(t, fixture, nil)
}

func TestChildRecoveryRegistersTheParentBeforeNestedChildrenRun(t *testing.T) {
	t.Parallel()
	fixture, parent, _ := recoveryFixture(t)
	ctx := context.Background()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("child recovered"), elelemtest.Text("parent recovered"))
	registry, err := NewStaticRegistry(map[string]ModelClient{runtimeTestModelReference: {Client: elelem.New(driver), Model: elelem.Model{ID: runtimeTestModelID}}})
	require.NoError(t, err)
	fixture.runtime.models = registry
	_, err = fixture.store.AppendAgentRunMessages(ctx, parent.SessionID, parent.ID, []session.AgentRunMessageInput{
		{Role: models.MessageRoleUser, Content: "parent task"},
		{Role: models.MessageRoleAssistant, ToolCallsJSON: `[{"ID":"collect","Name":"waitMessages","Arguments":{"timeout":"1m"}}]`},
	})
	require.NoError(t, err)
	_, err = fixture.store.AppendAgentRunEvent(ctx, parent.SessionID, parent.ID, session.AgentRunEventInput{EventType: EventTypeAgentRunToolUse, PayloadJSON: `{"callId":"collect","name":"waitMessages"}`})
	require.NoError(t, err)
	child, err := fixture.store.CreateAgentRun(ctx, parent.SessionID, session.StartAgentRunInput{
		ParentTurnID: parent.ParentTurnID, ParentAgentRunID: &parent.ID, RequestID: parent.RequestID,
		Name: "saved-nested", Definition: models.AgentRunDefinitionAdHoc, Depth: 2,
		Workspace: fixture.otherWorkspace, ModelReference: runtimeTestModelReference,
		ModelID: runtimeTestModelID, Task: "child task", Instructions: "child instructions",
		AllowedToolsJSON: `[]`, SystemPrompt: "child prompt",
	})
	require.NoError(t, err)
	_ = restartRecoveryFixture(t, fixture)
	awaitRecoveredChild(t, fixture, parent)
	awaitRecoveredChild(t, fixture, child)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	var conversation strings.Builder
	for _, message := range requests[1].Messages {
		conversation.WriteString(message.Text())
	}
	assert.Contains(t, conversation.String(), "child recovered")
	assert.Contains(t, conversation.String(), child.ID.String())
}

func restartRecoveryFixtureWithOptions(t *testing.T, fixture runtimeFixture, customize func(*RuntimeOptions)) *Runtime {
	t.Helper()
	ctx := context.Background()
	_, err := fixture.store.RecoverInterrupted(ctx)
	require.NoError(t, err)
	_, err = fixture.store.RecoverInterruptedAgentRuns(ctx)
	require.NoError(t, err)
	_, err = fixture.store.RecoverInterruptedModelRuns(ctx)
	require.NoError(t, err)
	options := RuntimeOptions{
		Store: fixture.store, Resolver: fixture.runtime.resolver, Models: fixture.runtime.models,
		RootAgent: runtimeTestAgentName, DefaultModel: runtimeTestModelReference,
		DefaultWorkspace: fixture.workspace, StartupSessionID: fixture.runtime.SessionID(),
		MaxContextTokens: runtimeTestMaxContextTokens, ConfigDirectory: fixture.configDirectory,
		TurnTimeout: runtimeTestTurnTimeout,
	}
	if customize != nil {
		customize(&options)
	}
	restarted, err := NewRuntime(ctx, options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restarted.ShutdownAgentRuns(ctx)) })

	return restarted
}

func TestChildRecoveryPublishesEventsWithTheSavedRequestIdentity(t *testing.T) {
	t.Parallel()
	fixture, run, _ := recoveryFixture(t)
	type observation struct {
		sessionID uuid.UUID
		event     Event
	}
	observed := make(chan observation, 64)
	_ = restartRecoveryFixtureWithOptions(t, fixture, func(options *RuntimeOptions) {
		options.ObserveEvent = func(id uuid.UUID, event Event) { observed <- observation{sessionID: id, event: event} }
	})
	awaitRecoveredChild(t, fixture, run)
	select {
	case received := <-observed:
		assert.Equal(t, run.SessionID, received.sessionID)
		assert.Equal(t, run.RequestID, received.event.RequestID)
		assert.Contains(t, string(received.event.Payload), run.ID.String())
	case <-time.After(time.Second):
		t.Fatal("the recovered child published no event")
	}
}

func awaitRecoveredChild(t *testing.T, fixture runtimeFixture, run *models.AgentRun) {
	t.Helper()
	require.Eventually(t, func() bool {
		stored, err := fixture.store.GetAgentRun(context.Background(), run.SessionID, run.ID)

		return err == nil && stored.State == models.AgentRunStateCompleted
	}, time.Second, time.Millisecond)
}

func TestChildRecoveryDoesNotRepeatUncheckpointedEffects(t *testing.T) {
	t.Parallel()
	fixture, run, driver := recoveryFixture(t)
	ctx := context.Background()
	calls, err := json.Marshal([]elelem.ToolCall{{ID: "uncertain", Name: toolNameRunCommand, Arguments: json.RawMessage(`{"command":"exit 99"}`)}})
	require.NoError(t, err)
	_, err = fixture.store.AppendAgentRunMessages(ctx, run.SessionID, run.ID, []session.AgentRunMessageInput{
		{Role: models.MessageRoleUser, Content: "saved task"},
		{Role: models.MessageRoleAssistant, ToolCallsJSON: string(calls)},
	})
	require.NoError(t, err)
	_ = restartRecoveryFixture(t, fixture)
	awaitRecoveredChild(t, fixture, run)
	requests := driver.Requests()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].Messages, 4)
	tool := requests[0].Messages[3]
	assert.Equal(t, elelem.RoleTool, tool.Role)
	assert.Equal(t, "uncertain", tool.ToolCallID)
	assert.True(t, tool.ToolResultIsError)
	assert.Contains(t, tool.Text(), "effects are unknown")
	jobs, err := fixture.store.ListJobs(ctx, run.SessionID, session.ListJobsOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items)
}

func TestChildRecoveryResumesQuestionWithoutSendingItTwice(t *testing.T) {
	t.Parallel()
	fixture, run, driver := recoveryFixture(t)
	ctx := context.Background()
	calls, err := json.Marshal([]elelem.ToolCall{{ID: "question", Name: toolNameAsk, Arguments: json.RawMessage(`{"question":"choose a branch","timeout":"1m"}`)}})
	require.NoError(t, err)
	_, err = fixture.store.AppendAgentRunMessages(ctx, run.SessionID, run.ID, []session.AgentRunMessageInput{
		{Role: models.MessageRoleUser, Content: "saved task"},
		{Role: models.MessageRoleAssistant, ToolCallsJSON: string(calls)},
	})
	require.NoError(t, err)
	for _, event := range []session.AgentRunEventInput{
		{EventType: EventTypeAgentRunToolUse, PayloadJSON: `{"callId":"question","name":"ask"}`},
		{EventType: EventTypeAgentRunQuestion, PayloadJSON: `{"question":"choose a branch"}`},
	} {
		_, err := fixture.store.AppendAgentRunEvent(ctx, run.SessionID, run.ID, event)
		require.NoError(t, err)
	}
	restarted := restartRecoveryFixture(t, fixture)
	result, err := restarted.RunMessage(ctx, MessageRequest{SessionID: &run.SessionID, To: &run.ID, Message: "use the saved branch"}, uuid.New(), nil)
	require.NoError(t, err)
	assert.True(t, result.Queued)
	awaitRecoveredChild(t, fixture, run)
	requests := driver.Requests()
	require.Len(t, requests, 1)
	var conversation strings.Builder
	for _, message := range requests[0].Messages {
		conversation.WriteString(message.Text())
	}
	assert.Contains(t, conversation.String(), "use the saved branch")
	events, err := fixture.store.ListAgentRunEvents(ctx, run.SessionID, run.ID, session.ListAgentRunEventsOptions{Limit: session.MaximumPageLimit})
	require.NoError(t, err)
	questions := 0
	for _, event := range events.Items {
		if event.EventType == EventTypeAgentRunQuestion {
			questions++
		}
	}
	assert.Equal(t, 1, questions)
}
