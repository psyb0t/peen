package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A parent launches a child, and that child itself launches a grandchild,
// proving launchAgent's recursion end to end. The grandchild's answer lands in
// the child's inbox as a tagged result, and the child's answer reaches the
// root as an event.
func TestLaunchAgentRecursion(t *testing.T) {
	driver := newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(launchAndWaitTurns(
			t,
			launchAgentCallID,
			launchAgentInput{Task: "delegate", Agent: launchAgentChildName},
			"parent final",
		)...),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(launchAndWaitTurns(
			t,
			launchAgentGrandCallID,
			launchAgentInput{
				Task:  "do the grandchild work",
				Agent: launchAgentGrandName,
			},
			"child final, saw: grandchild says hi",
		)...),
		launchAgentChildName: elelemtest.NewScriptedDriver(
			elelemtest.Text("grandchild says hi"),
		),
	}).
		after(runtimeTestAgentName, rootConversation, 2).
		after(launchAgentChildName, runtimeTestAgentName, 2)

	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
			launchAgentGrandName: launchAgentGrandAgentFile,
		},
	})

	turnEvents := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "start the chain",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&turnEvents),
	})
	require.NoError(t, err)
	assert.Equal(t, "parent final", result.Text)

	notice := onlyDeliveredNotice(t, turnEvents)
	data := agentEventData{}
	require.NoError(t, json.Unmarshal(notice.Data, &data))
	assert.Equal(t, "child final, saw: grandchild says hi", data.Response)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 2)

	byDepth := map[int]*AgentRun{}
	for _, run := range runs {
		byDepth[run.Depth] = run
	}

	require.Contains(t, byDepth, 1)
	require.Contains(t, byDepth, 2)
	assert.Equal(t, launchAgentChildName, byDepth[1].Name)
	assert.Equal(t, launchAgentGrandName, byDepth[2].Name)
	assert.Equal(t, AgentRunStateCompleted, byDepth[1].Snapshot().State)
	assert.Equal(t, AgentRunStateCompleted, byDepth[2].Snapshot().State)
	assert.Nil(t, byDepth[1].ParentAgentRunID)
	require.NotNil(t, byDepth[2].ParentAgentRunID)
	assert.Equal(t, byDepth[1].ID, *byDepth[2].ParentAgentRunID)
	assert.Equal(t, launchAgentCallID, byDepth[1].ParentToolCallID)
	assert.Equal(t, launchAgentGrandCallID, byDepth[2].ParentToolCallID)

	query := repositories.Use(fixture.handle.GormDB)
	storedChild, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(byDepth[1].ID)).
		First()
	require.NoError(t, err)
	assert.Nil(t, storedChild.ParentAgentRunID)
	assert.Equal(t, launchAgentCallID, storedChild.ParentToolCallID)

	storedGrandchild, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(byDepth[2].ID)).
		First()
	require.NoError(t, err)
	require.NotNil(t, storedGrandchild.ParentAgentRunID)
	assert.Equal(t, byDepth[1].ID, *storedGrandchild.ParentAgentRunID)
	assert.Equal(t, launchAgentGrandCallID, storedGrandchild.ParentToolCallID)

	grandchildResult, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: launchAgentGrandName,
		Run:  byDepth[2].ID.String(),
		Kind: messageKindResult,
		Text: "grandchild says hi",
	})
	require.NoError(t, err)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 3)
	assert.Equal(
		t,
		[]string{grandchildResult},
		tailTexts(childRequests[2].Messages, 1),
		"the grandchild's answer reaches the child after its wait",
	)

	grandchildRequests := driver.requests(launchAgentChildName)
	require.Len(t, grandchildRequests, 1)

	grandchildTask, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: launchAgentChildName,
		Run:  byDepth[1].ID.String(),
		Text: "do the grandchild work",
	})
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{grandchildTask},
		tailTexts(grandchildRequests[0].Messages, 1),
		"the grandchild's task names the child as its sender",
	)

	childTranscript, err := fixture.store.ListAgentRunMessages(
		context.Background(),
		result.SessionID,
		byDepth[1].ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)

	transcriptTexts := make([]string, 0, len(childTranscript.Items))
	for _, item := range childTranscript.Items {
		transcriptTexts = append(transcriptTexts, item.Content)
	}

	assert.Contains(
		t,
		transcriptTexts,
		grandchildResult,
		"the delivered result is part of the child's durable transcript",
	)
}

// A child at the configured limit cannot request more child work because the
// model never receives the launchAgent tool definition.
func TestLaunchAgentAtDepthLimitDoesNotExposeLaunchAgent(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "delegate", Agent: launchAgentChildName},
		elelemtest.Text("child final"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxDepth: 1},
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentDepthLimitedChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "start the chain",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	rootRequests := driver.requests(rootConversation)
	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.Contains(t, toolNames(rootRequests[0].Tools), toolNameLaunchAgent)
	assert.Contains(t, toolNames(childRequests[0].Tools), toolNameReadFile)
	assert.NotContains(
		t,
		toolNames(childRequests[0].Tools),
		toolNameLaunchAgent,
	)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(
		t,
		runs,
		1,
		"the depth-limited child must be the only registered run",
	)
	assert.Equal(t, launchAgentChildName, runs[0].Name)
	assert.Equal(t, AgentRunStateCompleted, runs[0].Snapshot().State)
}

// The tool is absent at the depth limit, but a stale provider response can
// still contain an old tool call. The handler itself must reject that call
// before it creates another child run.
func TestLaunchAgentRejectsUnadvertisedCallPastDepthLimit(t *testing.T) {
	fixture := newLaunchAgentFixture(
		t,
		elelemtest.NewScriptedDriver(),
		launchAgentFixtureOptions{
			AgentLimits: AgentRunLimits{MaxDepth: 1},
			AgentFiles: map[string]string{
				launchAgentChildName: launchAgentChildAgentFile,
			},
		},
	)

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	_, err = fixture.runtime.launchAgent(
		contextWithAgentDepth(
			context.Background(),
			fixture.runtime.agentLimits.MaxDepth,
		),
		&launchAgentDeps{
			snapshot:  snapshot,
			sessionID: uuid.New(),
		},
		launchAgentInput{
			Task:  "delegate",
			Agent: launchAgentChildName,
		},
	)
	require.ErrorIs(t, err, ErrAgentDepthExceeded)

	query := repositories.Use(fixture.handle.GormDB)
	agentRunCount, err := query.AgentRun.WithContext(context.Background()).Count()
	require.NoError(t, err)
	assert.Zero(t, agentRunCount)
}

func toolNames(tools []elelem.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	return names
}

// A low MaxChildTurns bound must withhold tools on the child's final round,
// proving the bound is threaded into the child's own request rather than
// inherited from the parent turn's larger MaxToolRounds.
func TestLaunchAgentMaxChildTurnsWithholdsToolsOnLastRound(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "say hi", Agent: launchAgentChildName},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxChildTurns: 1},
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.Empty(
		t,
		childRequests[0].Tools,
		"max child turns of 1 must withhold tools on the only round",
	)

	parentFirstRequest := driver.requests(rootConversation)[0]
	assert.NotEmpty(
		t,
		parentFirstRequest.Tools,
		"the parent's own round must still offer its full tool set",
	)
}
