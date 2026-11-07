package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allowed-tools removes workspace tools from a child, but never the messaging
// tools it needs to report back and to collect its own children's answers.
func TestLaunchAgentAllowedToolsExcludeWritesButKeepMessaging(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  "review the workspace",
			Agent: launchAgentChildName,
		},
		elelemtest.ToolCall(
			"call_restricted_write",
			toolNameWriteFile,
			`{"path":"must-not-exist.txt","content":"blocked"}`,
		),
		elelemtest.Text("review complete"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentRestrictedChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the reviewer",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	_, err = os.Stat(filepath.Join(fixture.workspace, "must-not-exist.txt"))
	assert.True(t, os.IsNotExist(err))

	childTools := toolNames(driver.requests(runtimeTestAgentName)[0].Tools)
	assert.NotContains(t, childTools, toolNameWriteFile)
	assert.Contains(t, childTools, toolNameReadFile)

	for _, messaging := range []string{
		toolNameSendMessage,
		toolNameWaitMessages,
		toolNameStopAgent,
		toolNameAsk,
		toolNameSearchExecutions,
		toolNameReadExecution,
		toolNameResumeExecution,
		toolNameResolveExecutionActivity,
	} {
		assert.Contains(t, childTools, messaging)
	}
}

func TestLaunchAgentAdHocDefinitionDeliversItsAnswer(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task: "say hi",
			AgentDefinition: &agentDefinitionInput{
				Name:         "scratch",
				Instructions: "do the ad-hoc task",
			},
		},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	turnEvents := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the ad-hoc child",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&turnEvents),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	data := agentEventData{}
	require.NoError(t, json.Unmarshal(
		onlyDeliveredNotice(t, turnEvents).Data,
		&data,
	))
	assert.Equal(t, "child says hi", data.Response)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	toolMessage := messages.Items[2]

	output := decodeLaunchAgentOutput(t, toolMessage.Content)
	assert.Equal(t, "scratch", output.Name)
	assert.Equal(t, AgentRunDefinitionAdHoc, output.Definition)
	assert.Equal(t, output.AgentRunID, data.RunID)
}

func TestLaunchAgentBothFormsIsValidationFailure(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: launchAgentChildName,
				AgentDefinition: &agentDefinitionInput{
					Name:         "scratch",
					Instructions: "do the task",
				},
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with both forms",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err, "a validation failure is a tool error, not a turn failure")
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "exactly one")
}

func TestLaunchAgentNeitherFormIsValidationFailure(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{Task: "say hi"}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with neither form",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "exactly one")
}

func TestLaunchAgentUnknownAgentListsEffectiveNames(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: "does-not-exist",
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch an unknown agent",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "not found")
	assert.Contains(t, toolResult.Content, launchAgentChildName)
}

const launchAgentCustomGeneralPurposeFile = "---\nname: general-purpose\ndescription: custom general purpose\n---\nCustom body."

// launchAgentToolResult runs one turn whose model launches the named agent
// and returns the launchAgent tool result. A launch expected to start a child
// waits for it. One expected to fail does not, because no child would ever
// answer.
func launchAgentToolResult(
	t *testing.T,
	agentName string,
	isLaunchExpected bool,
	options launchAgentFixtureOptions,
) toolResultPayload {
	t.Helper()

	input := launchAgentInput{Task: "say hi", Agent: agentName}

	var driver elelem.Driver = elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, input),
		),
		elelemtest.Text("done"),
	)
	if isLaunchExpected {
		driver = launchedChildDriver(t, input, elelemtest.Text("child says hi"))
	}

	fixture := newLaunchAgentFixture(t, driver, options)

	events := make([]Event, 0)
	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the agent",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))

	return toolResult
}

func TestLaunchAgentResolvesCoreAndCustomNames(t *testing.T) {
	testCases := []struct {
		name      string
		agent     string
		files     map[string]string
		wantAgent string
	}{
		{
			name:      "core name",
			agent:     "core:general-purpose",
			wantAgent: "core:general-purpose",
		},
		{
			name:      "plain name falls back to the core agent",
			agent:     "general-purpose",
			wantAgent: "core:general-purpose",
		},
		{
			name:      "plain name prefers the custom agent",
			agent:     "general-purpose",
			files:     map[string]string{"general-purpose": launchAgentCustomGeneralPurposeFile},
			wantAgent: "general-purpose",
		},
		{
			name:      "core name ignores the custom agent",
			agent:     "core:general-purpose",
			files:     map[string]string{"general-purpose": launchAgentCustomGeneralPurposeFile},
			wantAgent: "core:general-purpose",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := launchAgentToolResult(
				t,
				tc.agent,
				true,
				launchAgentFixtureOptions{AgentFiles: tc.files},
			)

			require.False(t, result.IsError, result.Content)
			assert.Equal(
				t,
				tc.wantAgent,
				decodeLaunchAgentOutput(t, result.Content).Name,
			)
		})
	}
}

// The child runs under the embedded agent's own instructions, and a custom
// agent of the same plain name changes nothing for the core: name.
func TestLaunchAgentCoreGeneralPurposeChildRunsOnTheEmbeddedInstructions(
	t *testing.T,
) {
	const embeddedBody = "Carry out the user's request within the resolved"

	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "say hi", Agent: "core:general-purpose"},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			"general-purpose": launchAgentCustomGeneralPurposeFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the agent",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)

	childSystem := childRequests[0].Messages[0].Text()
	assert.Contains(t, childSystem, embeddedBody)
	assert.NotContains(t, childSystem, "Custom body.")
	assert.Equal(
		t,
		[]string{taggedTask(t, "say hi")},
		tailTexts(childRequests[0].Messages, 1),
	)
}

func TestWorkspaceOnlyRootAgentStartsAndRunsATurn(t *testing.T) {
	const workspaceRoot = "workspace-root"

	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		Customize: func(options *RuntimeOptions) {
			options.RootAgent = workspaceRoot

			// Startup sees only the core agents and the config directory.
			resolver, err := harness.NewResolver(
				options.ConfigDirectory,
				harness.Limits{},
			)
			require.NoError(t, err)
			require.NoError(t, resolver.CheckRootAgent(workspaceRoot))
		},
	})

	writeRuntimeFile(
		t,
		filepath.Join(
			fixture.workspace,
			".agents",
			"agents",
			workspaceRoot+".md",
		),
		"---\nname: workspace-root\ndescription: workspace root\n---\nWorkspace root rules.",
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "hello",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)
	assert.Contains(
		t,
		driver.Requests()[0].Messages[0].Text(),
		"Workspace root rules.",
	)
}

func TestLaunchAgentRefusesADisabledCoreAgent(t *testing.T) {
	for _, agentName := range []string{
		"core:general-purpose",
		"general-purpose",
	} {
		t.Run(agentName, func(t *testing.T) {
			result := launchAgentToolResult(
				t,
				agentName,
				false,
				launchAgentFixtureOptions{
					Customize: func(options *RuntimeOptions) {
						resolver, err := harness.NewResolver(
							options.ConfigDirectory,
							harness.Limits{},
							harness.WithDisabledCoreAgents(
								[]string{"general-purpose"},
							),
						)
						require.NoError(t, err)

						options.Resolver = resolver
					},
				},
			)

			assert.True(t, result.IsError)
			assert.Contains(t, result.Content, "not found")
			assert.Contains(
				t,
				result.Content,
				"available agents: "+runtimeTestAgentName,
			)
		})
	}
}

func TestLaunchAgentOversizedAdHocInstructionsRejected(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task: "say hi",
				AgentDefinition: &agentDefinitionInput{
					Name:         "scratch",
					Instructions: strings.Repeat("x", 100),
				},
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxAdHocInstructionBytes: 8},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with an oversized ad-hoc definition",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "too large")
}
