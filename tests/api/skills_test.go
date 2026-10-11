//go:build integration

package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	skillsTestWorkspacePrefix = "skills-"

	skillsTestRunningMessage  = "read the fixture and then wait for me"
	skillsTestQueuedMessage   = "also follow :core:planning for the next step"
	skillsTestTurnStartText   = "plan this change with :core:planning"
	skillsTestFinalAnswer     = "the queued skill reached the model"
	skillsTestPlanningName    = "core:planning"
	skillsTestPlanningHeading = "# Planning"

	// skillsTestPlanningMessagePrefix is how every rendered core:planning
	// message begins, through the SKILL.md frontmatter's name line.
	skillsTestPlanningMessagePrefix = "<skill>\n<name>core:planning</name>\n" +
		"<description>Create evidence-based plans and test plans before any " +
		"request that changes something.</description>\n" +
		"<directory>embedded://skills/planning</directory>\n\n---\nname: planning\n"
	skillsTestMessageSuffix = "\n</skill>"

	skillsTestUnknownQueued    = "queue this with :no-such-skill in it"
	skillsTestUnknownTurnStart = "start this with :no-such-skill in it"
	skillsTestUnknownText      = `unknown skill "no-such-skill" in ` +
		`:no-such-skill; available skills: core:delegation, ` +
		`core:freshness, core:plan-and-delegate, core:planning`

	apiTestFrameUserMessageDelivered = "userMessage.delivered"
)

type skillInjectedFrame struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// A message queued during a turn that names a skill reaches the model after
// the tool result that ended the round: first the typed message, then the
// skill as its own message. Every client sees the delivery once, and the
// skill arrives as a skill.injected frame carrying the exact message.
func TestAPIQueuedSkillReachesTheModelAfterTheToolResult(t *testing.T) {
	workspace := openFreshWorkspace(t, skillsTestWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: skillsTestRunningMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name:      "readFile",
			Arguments: map[string]any{"path": workspace.fixturePath},
		}},
		FinalAnswer: skillsTestFinalAnswer,
	})

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(
		t,
		connection,
		workspace.sessionID,
		skillsTestRunningMessage,
	)
	awaitAPIWebSocketProviderHold(t, hold)

	queuer := openSessionConnection(t, workspace.sessionID)
	sendSessionMessage(
		t,
		queuer,
		workspace.sessionID,
		skillsTestQueuedMessage,
	)
	queued := awaitAPIWebSocketCompletion(t, queuer, true)

	hold.Release()

	frames := readFramesUntilTurnCompleted(t, connection)

	requests := providerRequestsSince(before)
	require.Len(t, requests, 2)

	second := nonSystemMessages(t, requests[1])
	require.Len(t, second, 5)
	assert.Equal(t, "user", second[0].Role)
	assert.Equal(t, skillsTestRunningMessage, second[0].Content)
	assert.Equal(t, "assistant", second[1].Role)
	assert.Equal(t, "tool", second[2].Role)
	assert.Equal(t, testinfra.ScriptedCallID(0), second[2].ToolCallID)
	assert.Equal(t, "user", second[3].Role)
	assert.Equal(t, skillsTestQueuedMessage, second[3].Content)
	assert.Equal(t, "user", second[4].Role)

	skillText := second[4].Content
	assert.True(t, strings.HasPrefix(skillText, skillsTestPlanningMessagePrefix))
	assert.Contains(t, skillText, skillsTestPlanningHeading)
	assert.True(t, strings.HasSuffix(skillText, skillsTestMessageSuffix))

	delivered := framesOfType(frames, apiTestFrameUserMessageDelivered)
	require.Len(t, delivered, 1)
	assert.Equal(t, queued.requestID, delivered[0].RequestID)

	injected := framesOfType(frames, apiTestFrameSkillInjected)
	require.Len(t, injected, 1)

	payload := skillInjectedFrame{}
	require.NoError(t, json.Unmarshal(injected[0].Data, &payload))
	assert.Equal(t, skillsTestPlanningName, payload.Name)
	assert.Equal(t, skillText, payload.Content)

	assertStoredQueuedSkill(t, workspace.sessionID, skillText)
}

func assertStoredQueuedSkill(
	t *testing.T,
	sessionID uuid.UUID,
	skillText string,
) {
	t.Helper()

	messages := storedMessages(t, sessionID)
	require.Len(t, messages, 6)

	assert.Equal(t, api.MessageRoleUser, messages[0].Role)
	assert.Equal(t, skillsTestRunningMessage, messages[0].Content)
	assert.Equal(t, api.MessageRoleAssistant, messages[1].Role)
	assert.Equal(t, api.MessageRoleTool, messages[2].Role)

	assert.Equal(t, api.MessageRoleUser, messages[3].Role)
	assert.Equal(t, skillsTestQueuedMessage, messages[3].Content)
	assert.Nil(t, messages[3].Injected)

	assert.Equal(t, api.MessageRoleUser, messages[4].Role)
	assert.Equal(t, skillText, messages[4].Content)
	require.NotNil(t, messages[4].Injected)
	assert.True(t, *messages[4].Injected)
	require.NotNil(t, messages[4].InjectedKind)
	assert.Equal(t, api.MessageInjectedKindSkill, *messages[4].InjectedKind)

	assert.Equal(t, api.MessageRoleAssistant, messages[5].Role)
	assert.Equal(t, skillsTestFinalAnswer, messages[5].Content)
}

// A skill a user names at the start of a turn is its own message after the
// typed one, never part of the system prompt, which only lists the skill.
func TestAPITurnStartSkillIsAMessageAndNotSystemPrompt(t *testing.T) {
	workspace := openFreshWorkspace(t, skillsTestWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(
		t,
		connection,
		workspace.sessionID,
		skillsTestTurnStartText,
	)
	frames := readFramesUntilTurnCompleted(t, connection)

	requests := providerRequestsSince(before)
	require.Len(t, requests, 1)

	messages := nonSystemMessages(t, requests[0])
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, skillsTestTurnStartText, messages[0].Content)
	assert.Equal(t, "user", messages[1].Role)

	skillText := messages[1].Content
	assert.Contains(t, skillText, skillsTestPlanningMessagePrefix)
	assert.Contains(t, skillText, skillsTestPlanningHeading)

	prompt := integrationInfra.LastSystemPrompt()
	assert.Contains(t, prompt, skillsTestPlanningName, "the catalogue lists it")
	assert.NotContains(t, prompt, skillsTestPlanningMessagePrefix)
	assert.NotContains(t, prompt, skillsTestPlanningHeading)

	injected := framesOfType(frames, apiTestFrameSkillInjected)
	require.Len(t, injected, 1)

	payload := skillInjectedFrame{}
	require.NoError(t, json.Unmarshal(injected[0].Data, &payload))
	assert.Equal(t, skillsTestPlanningName, payload.Name)
	assert.Equal(t, skillText, payload.Content)
}

// A queued message that names an unknown skill is refused with the same
// validation text a turn-start message gets, and it leaves nothing behind: the
// running turn never sees it and no row is stored for it.
func TestAPIQueuedUnknownSkillIsRefusedAndStoresNothing(t *testing.T) {
	workspace := openFreshWorkspace(t, skillsTestWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	hold, err := integrationInfra.HoldNextCompletion()
	require.NoError(t, err)
	t.Cleanup(hold.Release)

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(
		t,
		connection,
		workspace.sessionID,
		skillsTestRunningMessage,
	)
	awaitAPIWebSocketProviderHold(t, hold)

	queuer := openSessionConnection(t, workspace.sessionID)
	sendSessionMessage(
		t,
		queuer,
		workspace.sessionID,
		skillsTestUnknownQueued,
	)

	failure := awaitAPIWebSocketFailure(t, queuer)
	assert.Equal(
		t,
		aichteeteapee.ErrorCodeValidationFailed,
		failure.Code,
	)
	assert.Equal(t, skillsTestUnknownText, failure.Message)

	hold.Release()

	frames := readFramesIncludingFailures(t, connection)
	assert.Empty(t, framesOfType(frames, apiTestFrameUserMessageDelivered))
	assert.Empty(t, framesOfType(frames, apiTestFrameSkillInjected))

	broadcast := framesOfType(frames, apiTestWebSocketMessageFailed)
	require.Len(t, broadcast, 1, "the running turn's client sees the refusal")

	seen := apiTestWebSocketFailure{}
	require.NoError(t, json.Unmarshal(broadcast[0].Data, &seen))
	assert.Equal(t, failure, seen)

	for _, request := range providerRequestsSince(before) {
		for _, message := range request {
			assert.NotContains(t, message.Content, skillsTestUnknownQueued)
		}
	}

	messages := storedMessages(t, workspace.sessionID)
	require.Len(t, messages, 2)
	assert.Equal(t, skillsTestRunningMessage, messages[0].Content)
	assert.Equal(t, api.MessageRoleAssistant, messages[1].Role)
}

// An unknown skill named at the start of a turn refuses the message before
// anything is stored or sent to the model.
func TestAPITurnStartUnknownSkillStoresNothing(t *testing.T) {
	workspace := openFreshWorkspace(t, skillsTestWorkspacePrefix, nil)
	connection := openSessionConnection(t, workspace.sessionID)

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(
		t,
		connection,
		workspace.sessionID,
		skillsTestUnknownTurnStart,
	)

	failure := awaitAPIWebSocketFailure(t, connection)
	assert.Equal(
		t,
		aichteeteapee.ErrorCodeValidationFailed,
		failure.Code,
	)
	assert.Equal(t, skillsTestUnknownText, failure.Message)

	assert.Empty(t, storedMessages(t, workspace.sessionID))
	assert.Len(t, integrationInfra.ProviderRequests(), before)
	assert.Equal(t, int64(0), getSession(t, workspace.sessionID).MessageCount)
}
