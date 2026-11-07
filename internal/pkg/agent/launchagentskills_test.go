package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const launchAgentSkillTask = "review the workspace with :review-rules"

func TestLaunchAgentDescriptionTellsTheParentToNameSkillsInTheTask(
	t *testing.T,
) {
	t.Parallel()

	assert.Contains(t, launchAgentDescription, ":name")
	assert.Contains(t, launchAgentDescription, "skill")
	assert.Contains(t, launchAgentDescription, "unknown")
}

// A skill the user named on the parent reaches the child only when the parent
// names it in the task, so a child's system prompt never carries a skill.
func TestLaunchAgentDoesNotCopyTheUsersSkillIntoTheChild(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  "review the workspace",
			Agent: launchAgentChildName,
		},
		elelemtest.Text("child finished"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})
	writeSkillFile(
		t,
		fixture,
		runtimeTestExplicitSkillName,
		runtimeTestWorkspaceSkillInstructions,
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   runtimeTestExplicitSkillMessage,
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.NotContains(
		t,
		childRequests[0].Messages[0].Text(),
		runtimeTestWorkspaceSkillInstructions,
	)
	assert.Equal(
		t,
		[]string{taggedTask(t, "review the workspace")},
		tailTexts(childRequests[0].Messages, 1),
	)

	query := repositories.Use(fixture.handle.GormDB)
	storedRun, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)
	assert.NotContains(
		t,
		storedRun.SystemPrompt,
		runtimeTestWorkspaceSkillInstructions,
	)
}

// The task is treated like a user message: a :name in it brings the skill as
// its own message right after the task, in the child's first request and in
// the child's stored transcript.
func TestLaunchAgentTaskSkillReferenceFollowsTheTaskInTheChildRequest(
	t *testing.T,
) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  launchAgentSkillTask,
			Agent: launchAgentChildName,
		},
		elelemtest.Text("child finished"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})
	writeSkillFile(
		t,
		fixture,
		runtimeTestExplicitSkillName,
		runtimeTestWorkspaceSkillInstructions,
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the reviewer",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	skillMessage := expectedSkillMessage(
		t,
		fixture,
		runtimeTestExplicitSkillName,
	)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.Equal(
		t,
		[]string{taggedTask(t, launchAgentSkillTask), skillMessage},
		tailTexts(childRequests[0].Messages, 2),
	)
	assert.NotContains(
		t,
		childRequests[0].Messages[0].Text(),
		runtimeTestWorkspaceSkillInstructions,
	)
	assert.NotContains(
		t,
		driver.requests(rootConversation)[0].Messages[0].Text(),
		runtimeTestWorkspaceSkillInstructions,
	)

	query := repositories.Use(fixture.handle.GormDB)
	storedRun, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)
	assert.NotContains(
		t,
		storedRun.SystemPrompt,
		runtimeTestWorkspaceSkillInstructions,
	)

	transcript, err := fixture.store.ListAgentRunMessages(
		context.Background(),
		result.SessionID,
		storedRun.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)
	require.Len(t, transcript.Items, 3)

	contents := make([]string, 0, len(transcript.Items))
	roles := make([]models.MessageRole, 0, len(transcript.Items))

	for _, item := range transcript.Items {
		contents = append(contents, item.Content)
		roles = append(roles, item.Role)
	}

	assert.Equal(
		t,
		[]string{
			taggedTask(t, launchAgentSkillTask),
			skillMessage,
			"child finished",
		},
		contents,
	)
	assert.Equal(
		t,
		[]models.MessageRole{
			models.MessageRoleUser,
			models.MessageRoleUser,
			models.MessageRoleAssistant,
		},
		roles,
	)
}

// A skill the child's task names is checked before the child exists. An
// unknown name fails the launchAgent call and lists the effective skills.
func TestLaunchAgentUnknownTaskSkillFailsTheCall(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "review with :ghost-skill",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})
	writeSkillFile(
		t,
		fixture,
		runtimeTestExplicitSkillName,
		runtimeTestWorkspaceSkillInstructions,
	)

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the reviewer",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err, "an unknown skill is a tool error, not a turn failure")
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(
		t,
		toolResult.Content,
		`unknown skill "ghost-skill" in :ghost-skill; available skills: `,
	)
	assert.Contains(t, toolResult.Content, runtimeTestExplicitSkillName)
	assert.Len(t, driver.Requests(), 2, "the child never ran")
}
