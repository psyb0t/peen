//go:build integration

package api_test

import (
	"encoding/json"
	"path"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/require"
)

const (
	freshWorkspaceFixtureFile    = "README.md"
	freshWorkspaceFixtureContent = "a workspace only this test opens"

	apiTestFrameSkillInjected = "skill.injected"
)

// freshWorkspace is a workspace directory only one test opens, so its session,
// harness files, jobs, and notices cannot be touched by any other test.
type freshWorkspace struct {
	sessionID uuid.UUID
	directory string
	// fixturePath is an absolute path to a file the workspace holds, which a
	// scripted read_file call can read.
	fixturePath string
}

// openFreshWorkspace writes files below a new workspace directory, then opens
// a session on it. The keys of files are paths relative to the workspace.
func openFreshWorkspace(
	t *testing.T,
	prefix string,
	files map[string]string,
) freshWorkspace {
	t.Helper()

	directory := prefix + uuid.NewString()

	all := map[string]string{
		freshWorkspaceFixtureFile: freshWorkspaceFixtureContent,
	}
	for name, content := range files {
		all[name] = content
	}

	for name, content := range all {
		require.NoError(t, integrationInfra.WriteWorkspaceFile(
			t.Context(),
			path.Join(directory, name),
			[]byte(content),
		))
	}

	absolute := path.Join(testinfra.ContainerWorkingDirectory, directory)

	return freshWorkspace{
		sessionID:   openAPIWorkspaceSessionAt(t, absolute),
		directory:   absolute,
		fixturePath: path.Join(absolute, freshWorkspaceFixtureFile),
	}
}

// apiTestFrame is one WebSocket frame a turn produced.
type apiTestFrame struct {
	Type      string
	Data      json.RawMessage
	RequestID uuid.UUID
}

// openSessionConnection dials the WebSocket filtered to one session.
func openSessionConnection(
	t *testing.T,
	sessionID uuid.UUID,
) *websocket.Conn {
	t.Helper()

	connection := dialAPIWebSocket(t, &sessionID)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	return connection
}

func sendSessionMessage(
	t *testing.T,
	connection *websocket.Conn,
	sessionID uuid.UUID,
	message string,
) {
	t.Helper()

	require.NoError(t, writeAPIWebSocketSessionMessage(
		connection,
		sessionID,
		map[string]string{"message": message},
	))
}

// readFramesUntilTurnCompleted reads every frame up to and including the
// message.completed of a message that ran as a turn, not a queued one. It
// fails the test on a message.failed.
func readFramesUntilTurnCompleted(
	t *testing.T,
	connection *websocket.Conn,
) []apiTestFrame {
	t.Helper()

	return readFrames(t, connection, false)
}

// readFramesIncludingFailures is readFramesUntilTurnCompleted for a test that
// expects a message.failed in the stream. Every client of a session receives
// the failures of every message sent to it.
func readFramesIncludingFailures(
	t *testing.T,
	connection *websocket.Conn,
) []apiTestFrame {
	t.Helper()

	return readFrames(t, connection, true)
}

func readFrames(
	t *testing.T,
	connection *websocket.Conn,
	allowFailure bool,
) []apiTestFrame {
	t.Helper()

	frames := make([]apiTestFrame, 0)

	for {
		received := readWebSocketEvent(t, connection)
		frame := apiTestFrame{
			Type: string(received.Type),
			Data: received.Data,
		}

		if received.Metadata != nil {
			if value, found := received.Metadata.Get(
				apiTestWebSocketMetadataRequestID,
			); found {
				text, _ := value.(string)
				frame.RequestID, _ = uuid.Parse(text)
			}
		}

		frames = append(frames, frame)

		if frame.Type == apiTestWebSocketMessageFailed && !allowFailure {
			t.Fatalf("WebSocket message failed: %s", frame.Data)
		}

		if frame.Type != apiTestWebSocketMessageCompleted {
			continue
		}

		result := apiTestWebSocketResult{}
		require.NoError(t, json.Unmarshal(frame.Data, &result))

		if !result.Queued {
			return frames
		}
	}
}

func framesOfType(frames []apiTestFrame, frameType string) []apiTestFrame {
	matching := make([]apiTestFrame, 0)

	for _, frame := range frames {
		if frame.Type == frameType {
			matching = append(matching, frame)
		}
	}

	return matching
}

// providerRequestsSince returns the completion requests the base provider
// mock received after the first `before` of them.
func providerRequestsSince(before int) [][]testinfra.ProviderMessage {
	return integrationInfra.ProviderRequests()[before:]
}

// nonSystemMessages drops the system prompt, which every request starts with.
func nonSystemMessages(
	t *testing.T,
	messages []testinfra.ProviderMessage,
) []testinfra.ProviderMessage {
	t.Helper()
	require.NotEmpty(t, messages)
	require.Equal(t, "system", messages[0].Role)

	return messages[1:]
}

// storedMessages returns the session's durable messages, oldest first.
func storedMessages(t *testing.T, sessionID uuid.UUID) []api.Message {
	t.Helper()

	return collectAllMessages(t, sessionID)
}
