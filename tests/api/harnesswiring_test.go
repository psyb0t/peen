//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/aichteeteapee"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	hooksTestWorkspacePrefix = "hooks-"
	hooksTestFile            = ".agents/hooks.yaml"
	hooksTestMessage         = "read the fixture so the hooks fire"
	hooksTestFinalAnswer     = "the hooks fired"

	hooksTestYAML = `version: 1
turn_start:
  - name: api-turn-start
    actions:
      - name: remind-turn
        type: inject
        message: Hook text for the turn start.
post_tool_use:
  - name: api-after-read
    match:
      tool: read_file
    actions:
      - name: remind-read
        type: inject
        message: Hook text after a read.
`
	hooksTestTurnStartBlock = "<hook>\n<event>turn_start</event>\n\n" +
		"Hook text for the turn start.\n</hook>"
	hooksTestPostToolBlock = "<hook>\n<event>post_tool_use</event>\n" +
		"<tool>read_file</tool>\n\nHook text after a read.\n</hook>"

	handlerTestWorkspacePrefix = "handler-"
	handlerTestEventType       = "test.api.handler"
	handlerTestFile            = ".agents/events/" + handlerTestEventType + ".md"
	handlerTestDocument        = "---\ntype: " + handlerTestEventType +
		"\ndelivery: wake\nagent: core:general-purpose\n---\n" +
		"Say that the handler reached you.\n"
	handlerTestNoticeSummary = "an outside system needs the agent"
	handlerTestNoticeBody    = `{"type":"` + handlerTestEventType +
		`","summary":"` + handlerTestNoticeSummary + `"}`
	handlerTestBlock = "<handler>\n<event>" + handlerTestEventType +
		"</event>\n<agent>core:general-purpose</agent>\n\n" +
		"Say that the handler reached you.\n\n" +
		"Handle this as the core:general-purpose agent would.\n</handler>"
	handlerTestEventsPrefix = "<events>"

	variantTestStartTimeout = 3 * time.Minute
	variantTestWorkspace    = testinfra.ContainerWorkingDirectory
	variantTestSkillsOff    = "planning"
	variantTestAgentsOff    = "general-purpose"
	variantTestMessage      = "say hello in this variant"
	variantTestAgentBody    = "Carry out the user's request within the " +
		"resolved instructions and tool\ncontracts."

	variantTestFreshnessEntry = "- core:freshness: Verify time-sensitive " +
		"facts with current authoritative evidence before using them."
	variantTestPlanningEntry = "- core:planning: Create evidence-based " +
		"plans and test plans before any request that changes something."
	variantTestGeneralPurposeEntry = "- core:general-purpose: " +
		"General-purpose agent for coding and other repository tasks."
	variantTestCustomBody  = "Follow the request and return the result."
	variantTestAgentSetter = "PEEN_AGENT"
	variantTestSkillsEnv   = "PEEN_DISABLED_CORE_SKILLS"
	variantTestAgentsEnv   = "PEEN_DISABLED_CORE_AGENTS"
	variantTestDisabledMsg = "use :core:planning here"
	variantTestDisabledErr = `unknown skill "core:planning" in ` +
		`:core:planning; available skills: core:freshness`
	variantTestFreshnessMsg = "use :core:freshness here"
)

// A hook inject in the workspace's hooks.yaml reaches the model from the
// session's worker: a turn_start inject joins the system prompt, and a
// post_tool_use inject follows the tool result as its own user message.
func TestAPIWorkspaceHooksInjectIntoTheWorkerTurn(t *testing.T) {
	workspace := openFreshWorkspace(t, hooksTestWorkspacePrefix, map[string]string{
		hooksTestFile: hooksTestYAML,
	})
	connection := openSessionConnection(t, workspace.sessionID)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: hooksTestMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name:      "read_file",
			Arguments: map[string]any{"path": workspace.fixturePath},
		}},
		FinalAnswer: hooksTestFinalAnswer,
	})

	before := len(integrationInfra.ProviderRequests())

	sendSessionMessage(t, connection, workspace.sessionID, hooksTestMessage)
	readFramesUntilTurnCompleted(t, connection)

	requests := providerRequestsSince(before)
	require.Len(t, requests, 2)

	assert.Equal(t, "system", requests[0][0].Role)
	assert.True(
		t,
		strings.HasSuffix(requests[0][0].Content, hooksTestTurnStartBlock),
		"the turn_start inject ends the system prompt",
	)

	second := nonSystemMessages(t, requests[1])
	require.Len(t, second, 4)
	assert.Equal(t, "user", second[0].Role)
	assert.Equal(t, hooksTestMessage, second[0].Content)
	assert.Equal(t, "assistant", second[1].Role)
	assert.Equal(t, "tool", second[2].Role)
	assert.Equal(t, "user", second[3].Role)
	assert.Equal(t, hooksTestPostToolBlock, second[3].Content)
	assert.Contains(t, requests[1][0].Content, hooksTestTurnStartBlock)
}

// A handler-started turn begins with the exact handler block, both in the
// message the session stores and in the request the model receives, and the
// event that caused the wake arrives with it as quoted data.
func TestAPIHandlerBlockReachesTheWokenTurn(t *testing.T) {
	workspace := openFreshWorkspace(t, handlerTestWorkspacePrefix, map[string]string{
		handlerTestFile: handlerTestDocument,
	})
	connection := openSessionConnection(t, workspace.sessionID)

	before := len(integrationInfra.ProviderRequests())

	response := apiRequest(
		t,
		http.MethodPost,
		sessionNoticesPath,
		[]byte(handlerTestNoticeBody),
		withHeader(
			authenticatedHeaders(),
			headerSessionID,
			workspace.sessionID.String(),
		),
	)
	requireAPIStatus(t, response, http.StatusAccepted)

	awaitWebSocketEvent(t, connection, apiTestWebSocketTurnCompleted)

	requests := providerRequestsSince(before)
	require.Len(t, requests, 1)

	messages := nonSystemMessages(t, requests[0])
	require.GreaterOrEqual(t, len(messages), 2)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, handlerTestBlock, messages[0].Content)
	assert.Equal(t, "user", messages[1].Role)
	assert.Contains(t, messages[1].Content, handlerTestEventsPrefix)
	assert.Contains(t, messages[1].Content, handlerTestNoticeSummary)

	// The handler block and the events message are both stored as injected
	// user messages, in the order the provider received them.
	stored := make([]string, 0, len(messages))

	for _, message := range storedMessages(t, workspace.sessionID) {
		if message.Role != api.MessageRoleUser {
			continue
		}

		require.NotNil(t, message.Injected)
		assert.True(t, *message.Injected)

		stored = append(stored, message.Content)
	}

	require.Len(t, stored, 2)
	assert.Equal(t, messages[0].Content, stored[0])
	assert.Equal(t, messages[1].Content, stored[1])
	assert.Equal(t, handlerTestBlock, stored[0])
	assert.Contains(t, stored[1], handlerTestEventsPrefix)
}

// PEEN_DISABLED_CORE_SKILLS and PEEN_DISABLED_CORE_AGENTS apply inside the
// worker a session runs in: the disabled skill leaves the system prompt's
// catalogue and `:core:<name>` for it is refused, while the other core skill
// still works.
func TestAPIDisabledCoreSkillsAndAgentsApplyInsideTheWorker(t *testing.T) {
	variant := startAPIVariant(t, testinfra.VariantOptions{
		Environment: map[string]string{
			variantTestSkillsEnv: variantTestSkillsOff,
			variantTestAgentsEnv: variantTestAgentsOff,
		},
	})
	sessionID := openVariantSession(t, variant)
	connection := dialVariantSession(t, variant, sessionID)

	sendSessionMessage(t, connection, sessionID, variantTestMessage)
	readFramesUntilTurnCompleted(t, connection)

	prompt := variant.LastSystemPrompt()
	assert.Contains(t, prompt, variantTestFreshnessEntry)
	assert.NotContains(t, prompt, variantTestPlanningEntry)
	assert.NotContains(t, prompt, variantTestGeneralPurposeEntry)

	sendSessionMessage(t, connection, sessionID, variantTestDisabledMsg)

	failure := awaitAPIWebSocketFailure(t, connection)
	assert.Equal(
		t,
		string(aichteeteapee.ErrorCodeValidationFailed),
		failure.Code,
	)
	assert.Equal(t, variantTestDisabledErr, failure.Message)

	sendSessionMessage(t, connection, sessionID, variantTestFreshnessMsg)
	readFramesUntilTurnCompleted(t, connection)
}

// With PEEN_AGENT unset the root agent is the embedded core:general-purpose.
func TestAPIDefaultRootAgentIsTheCoreGeneralPurposeAgent(t *testing.T) {
	variant := startAPIVariant(t, testinfra.VariantOptions{
		UnsetEnvironment: []string{variantTestAgentSetter},
	})
	sessionID := openVariantSession(t, variant)
	connection := dialVariantSession(t, variant, sessionID)

	sendSessionMessage(t, connection, sessionID, variantTestMessage)
	readFramesUntilTurnCompleted(t, connection)

	prompt := variant.LastSystemPrompt()
	assert.Contains(t, prompt, variantTestAgentBody)
	assert.NotContains(t, prompt, variantTestCustomBody)
}

func startAPIVariant(
	t *testing.T,
	options testinfra.VariantOptions,
) *testinfra.Infra {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), variantTestStartTimeout)
	defer cancel()

	variant, err := integrationInfra.StartVariant(ctx, options)
	require.NoError(t, err)

	t.Cleanup(func() {
		teardown, cancelTeardown := context.WithTimeout(
			context.Background(),
			requestTimeout,
		)
		defer cancelTeardown()

		require.NoError(t, variant.Teardown(teardown))
	})

	return variant
}

func openVariantSession(t *testing.T, variant *testinfra.Infra) uuid.UUID {
	t.Helper()

	body, err := json.Marshal(map[string]string{
		"workspace": variantTestWorkspace,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		variant.APIURL(apiBasePath+"/sessions/open"),
		bytes.NewReader(body),
	)
	require.NoError(t, err)

	for name, value := range authenticatedHeaders() {
		request.Header.Set(name, value)
	}

	response, err := variant.HTTPClient().Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)

	opened := decodeResponse[apiTestOpenedSession](t, response)

	return opened.Session.ID
}

func dialVariantSession(
	t *testing.T,
	variant *testinfra.Infra,
	sessionID uuid.UUID,
) *websocket.Conn {
	t.Helper()

	endpoint, err := url.Parse(variant.APIURL(apiTestWebSocketPath))
	require.NoError(t, err)

	endpoint.Scheme = "ws"
	query := endpoint.Query()
	query.Set(apiTestWebSocketSessionIDParameter, sessionID.String())
	endpoint.RawQuery = query.Encode()

	dialer := apiTestWebSocketDialer()
	connection, response, err := dialer.Dial(endpoint.String(), nil)
	if response != nil {
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	}

	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	return connection
}
