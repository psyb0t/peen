//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	systemSessionPath  = apiBasePath + "/sessions/system"
	systemAgentName    = "core:system"
	systemAgentMessage = "report the deployment's model time"
	systemAgentPlain   = "a plain message to the system session"
	systemAgentBinary  = "/app/app"

	systemAgentCommandMessage = "report the deployment from the command line"

	systemAgentAnswer     = "the system agent is done"
	systemAgentIdentity   = "system agent of this Peen deployment"
	systemAgentToolRecord = "system agent tool call"
	systemAgentAuditWait  = time.Minute
	systemAgentAuditTick  = 250 * time.Millisecond
)

func openSystemSession(t *testing.T) api.OpenedSession {
	t.Helper()

	response := apiRequest(
		t,
		http.MethodPost,
		systemSessionPath,
		nil,
		authenticatedHeaders(),
	)
	requireAPIStatus(t, response, http.StatusOK)

	return decodeResponse[api.OpenedSession](t, response)
}

func sendSystemMessage(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	message string,
) {
	t.Helper()

	event := dabluveees.NewEvent(
		apiTestWebSocketMessageSend,
		map[string]any{"message": message, "system": true},
	).SetMetadata(
		apiTestWebSocketSessionIDParameter,
		sessionID.String(),
	)

	assertValidMessageSend(t, event)
	require.NoError(t, connection.WriteJSON(event))
}

// requireMessageRefused reads frames until the message fails. A refused message
// starts no turn, so nothing else ends the stream.
func requireMessageRefused(t *testing.T, connection *websocket.Conn) {
	t.Helper()

	for {
		frameType := string(readWebSocketEvent(t, connection).Type)

		require.NotEqual(t, apiTestWebSocketMessageCompleted, frameType)

		if frameType == apiTestWebSocketMessageFailed {
			return
		}
	}
}

func TestAPISystemSessionIsOneControlSessionWithTheReservedAgent(t *testing.T) {
	first := openSystemSession(t)
	second := openSystemSession(t)

	assert.Equal(t, first.Session.Id, second.Session.Id)
	assert.False(t, second.Created)
	assert.Equal(t, systemAgentName, first.Session.Agent)
	require.NotNil(t, first.Session.AgentSpace)
	assert.Equal(t, "control", *first.Session.AgentSpace)
}

// The real path: open the session, send a system frame, and let a scripted
// model call a tool only the deployment-wide reader answers across sessions.
func TestAPISystemTurnRunsReadsMetricsAndIsAudited(t *testing.T) {
	opened := openSystemSession(t)
	connection := openSessionConnection(t, opened.Session.Id)

	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: systemAgentMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name:      readMetricsToolName,
			Arguments: map[string]any{"summary": "sessions"},
		}},
		FinalAnswer: systemAgentAnswer,
	})

	sendSystemMessage(t, connection, opened.Session.Id, systemAgentMessage)

	frames := readFramesUntilTurnCompleted(t, connection)

	var transcript strings.Builder

	for _, frame := range frames {
		encoded, err := json.Marshal(frame.Data)
		require.NoError(t, err)

		transcript.Write(encoded)
	}

	assert.Contains(t, transcript.String(), systemAgentAnswer)
	assert.Contains(t, integrationInfra.LastSystemPrompt(), systemAgentIdentity)

	require.Eventually(t, func() bool {
		logs, err := integrationInfra.AppLogs(t.Context())
		if err != nil {
			return false
		}

		return strings.Contains(logs, systemAgentToolRecord)
	}, systemAgentAuditWait, systemAgentAuditTick)
}

// runSystemCommand runs the real binary in the controller's container, against
// the controller already running there.
func runSystemCommand(t *testing.T, arguments ...string) (int, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), integrationTimeout)
	defer cancel()

	command := append([]string{systemAgentBinary, "system"}, arguments...)

	exitCode, output, err := integrationInfra.App.Exec(ctx, command)
	require.NoError(t, err)

	content, err := io.ReadAll(output)
	require.NoError(t, err)

	return exitCode, string(content)
}

func TestSystemCommandRunsATurnInTheSystemSession(t *testing.T) {
	t.Cleanup(integrationInfra.DisableScriptedToolTurn)
	integrationInfra.EnableScriptedToolTurn(testinfra.ScriptedToolTurn{
		UserMessage: systemAgentCommandMessage,
		Calls: []testinfra.ScriptedToolCall{{
			Name:      readMetricsToolName,
			Arguments: map[string]any{"summary": "sessions"},
		}},
		FinalAnswer: systemAgentAnswer,
	})

	exitCode, output := runSystemCommand(t, systemAgentCommandMessage)

	require.Zero(t, exitCode, output)
	assert.Contains(t, output, systemAgentAnswer)
	assert.Contains(t, integrationInfra.LastSystemPrompt(), systemAgentIdentity)
}

func TestSystemCommandRefusesAnEmptyPrompt(t *testing.T) {
	exitCode, _ := runSystemCommand(t, "   ")

	assert.NotZero(t, exitCode)
}

func TestAPISystemSessionRefusesAPlainFrame(t *testing.T) {
	opened := openSystemSession(t)
	connection := openSessionConnection(t, opened.Session.Id)

	sendSessionMessage(t, connection, opened.Session.Id, systemAgentPlain)

	requireMessageRefused(t, connection)
}

func TestAPIOrdinarySessionRefusesASystemFrame(t *testing.T) {
	workspace := openFreshWorkspace(t, "system-frame-", nil)
	connection := openSessionConnection(t, workspace.sessionID)

	sendSystemMessage(t, connection, workspace.sessionID, systemAgentPlain)

	requireMessageRefused(t, connection)
}

func TestAPISystemSessionStaysInControl(t *testing.T) {
	opened := openSystemSession(t)

	body, err := json.Marshal(api.ReconfigureSessionRequest{
		AgentSpace: "docker:default",
		Reason:     "move the system agent",
	})
	require.NoError(t, err)

	headers := authenticatedHeaders()
	headers[headerContentType] = jsonMediaType
	headers[headerSessionID] = opened.Session.Id.String()

	response := apiRequest(
		t,
		http.MethodPost,
		sessionPath+"/reconfigure",
		body,
		headers,
	)
	requireAPIStatus(t, response, http.StatusForbidden)
}
