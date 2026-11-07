//go:build real

package realtest

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	realSystemMetricsMarker = "REAL_SYSTEM_METRICS_MARKER"
	realSystemEditMarker    = "REAL_SYSTEM_EDIT_MARKER"

	realSystemMetricsTask = "Call readMetrics with the sessions summary, then " +
		"reply with exactly " + realSystemMetricsMarker + "."
	realSystemEditTask = "Add a line holding " + realSystemEditMarker +
		" to AGENTS.md in the configuration directory."
	realSystemApproval = "Yes, make that change."

	realSystemSetting      = "PEEN_SYSTEM_AGENT"
	realSystemConfigFile   = "AGENTS.md"
	realSystemMetricsTool  = "readMetrics"
	realSystemQuestionType = "question.asked"
	realSystemTurnEndType  = "turn.completed"
	realSystemTurnFailType = "turn.failed"
)

type realSystemMessage struct {
	Message string `json:"message"`
	System  bool   `json:"system"`
}

type realSystemTurn struct {
	process    *runningRealHarnessPeen
	fixture    realHarnessFixture
	sessionID  uuid.UUID
	connection *websocket.Conn
}

func startRealSystemTurn(t *testing.T) realSystemTurn {
	t.Helper()

	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	encodedUpstreams, err := json.Marshal(upstreams)
	require.NoError(t, err)

	fixture := newRealHarnessFixture(t)
	process := startRealHarnessPeenWith(
		t,
		buildRealHarnessBinary(t),
		fixture,
		string(encodedUpstreams),
		configured.DefaultModel,
		map[string]string{realSystemSetting: "true"},
	)
	t.Cleanup(func() { process.stop(t) })

	connection := dialRealHarnessWebSocket(t, process)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	return realSystemTurn{
		process:    process,
		fixture:    fixture,
		sessionID:  openRealSystemSession(t, process),
		connection: connection,
	}
}

func openRealSystemSession(
	t *testing.T,
	process *runningRealHarnessPeen,
) uuid.UUID {
	t.Helper()

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		process.baseURL+realHarnessAPIPath+"/sessions/system",
		nil,
	)
	require.NoError(t, err)
	request.Header.Set(
		realHarnessAuthorization,
		realHarnessBearerPrefix+process.apiToken,
	)

	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)

	defer func() { require.NoError(t, response.Body.Close()) }()

	require.Equalf(
		t,
		http.StatusOK,
		response.StatusCode,
		"Peen output:\n%s",
		process.output.String(),
	)

	opened := api.OpenedSession{}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&opened))

	return opened.Session.Id
}

func (s realSystemTurn) send(t *testing.T, message string) {
	t.Helper()

	event := dabluveees.NewEvent(
		realHarnessMessageSendEvent,
		realSystemMessage{Message: message, System: true},
	).SetMetadata(realHarnessWebSocketSessionID, s.sessionID.String())
	require.NoError(t, s.connection.WriteJSON(event))
}

// await reads frames until the turn ends, calling onQuestion for each question
// the model asks.
func (s realSystemTurn) await(t *testing.T, onQuestion func()) {
	t.Helper()

	for {
		event := readRealHarnessWebSocketEvent(t, s.process, s.connection)

		switch string(event.Type) {
		case realSystemQuestionType:
			onQuestion()
		case realSystemTurnEndType:
			return
		case realSystemTurnFailType:
			require.FailNow(
				t,
				"the system turn failed",
				"Peen output:\n%s",
				s.process.output.String(),
			)
		}
	}
}

func TestRealSystemAgentReadsMetrics(t *testing.T) {
	turn := startRealSystemTurn(t)

	turn.send(t, realSystemMetricsTask)
	turn.await(t, func() {})

	messages := listRealHarnessMessages(
		t,
		turn.process.baseURL,
		turn.process.apiToken,
		turn.sessionID,
	)

	assert.Contains(t, realHarnessToolCallNames(messages), realSystemMetricsTool)
}

// system.md tells the model to get agreement before it changes the
// configuration, and that holds only as far as a real model follows it.
func TestRealSystemAgentAsksBeforeChangingTheConfiguration(t *testing.T) {
	turn := startRealSystemTurn(t)
	target := filepath.Join(turn.fixture.configDirectory, realSystemConfigFile)
	isAsked := false

	turn.send(t, realSystemEditTask)
	turn.await(t, func() {
		isAsked = true

		content, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.NotContains(t, string(content), realSystemEditMarker)

		turn.send(t, realSystemApproval)
	})

	require.True(t, isAsked, "the model changed the file without asking")

	content, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Contains(t, string(content), realSystemEditMarker)
}
