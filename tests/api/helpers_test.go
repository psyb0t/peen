//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/tests/testinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	freshWorkspaceFixtureFile    = "README.md"
	freshWorkspaceFixtureContent = "a workspace only this test opens"

	apiTestFrameSkillInjected = "skill.injected"
)

const apiTestToolOutputOpen = "<toolOutput tool="

// toolOutputText returns the result text inside the <toolOutput> wrapper Peen
// puts around a tool result, and fails the test when it is missing.
func toolOutputText(t *testing.T, content string) string {
	t.Helper()

	require.True(t, strings.HasPrefix(content, apiTestToolOutputOpen), content)

	return testinfra.ToolOutputText(content)
}

// freshWorkspace is a workspace directory only one test opens, so its session,
// harness files, jobs, and notices cannot be touched by any other test.
type freshWorkspace struct {
	sessionID uuid.UUID
	directory string
	// fixturePath is an absolute path to a file the workspace holds, which a
	// scripted readFile call can read.
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
	// Sequence is the frame's feed sequence. Only meaningful when HasSequence
	// is true: message.completed and message.failed carry none.
	Sequence    int64
	HasSequence bool
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
		t,
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

		frame.Sequence, frame.HasSequence = apiTestFrameSequence(received)

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

// hostToolsContainerPath places a scenario fixture under the app's immutable
// tool workspace, so a relative tool path would resolve here too.
func hostToolsContainerPath(name string) string {
	return path.Join(testinfra.ContainerWorkingDirectory, name)
}

// seedContainerFile writes content to containerPath through the running
// app container's own shell, via a quoted heredoc so no shell metacharacter
// in content is ever interpreted. That makes the file's owner the app's own
// runtime user, matching a file the app wrote itself.
func seedContainerFile(t *testing.T, containerPath, content string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	script := "cat > " + shellSingleQuote(containerPath) + " <<'" +
		hostToolsFixtureHeredoc + "'\n" + content + hostToolsFixtureHeredoc + "\n"

	exitCode, output, err := integrationInfra.App.Exec(
		ctx,
		[]string{"sh", "-c", script},
	)
	require.NoError(t, err)

	outputBytes, readErr := io.ReadAll(output)
	require.NoError(t, readErr)
	require.Equal(t, 0, exitCode, "seed fixture file: %s", outputBytes)
}

// shellSingleQuote wraps value for safe use as one POSIX shell word.
func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func readContainerFile(t *testing.T, containerPath string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	reader, err := integrationInfra.App.CopyFileFromContainer(ctx, containerPath)
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, reader.Close())
	}()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	return string(data)
}

// collectAllMessages walks every page of a session's message list with a
// page size smaller than the total, rather than trusting page one alone.
func collectAllMessages(t *testing.T, sessionID uuid.UUID) []api.Message {
	t.Helper()

	messages := make([]api.Message, 0, hostToolsMessageCount)
	offset := 0

	for {
		page := listMessages(t, sessionID, hostToolsPageLimit, offset, "asc")
		messages = append(messages, page.Items...)
		offset += len(page.Items)

		if !page.HasMore {
			break
		}
	}

	return messages
}

func awaitFixtureCommand(t *testing.T, command ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	ticker := time.NewTicker(jobNoticePoll)
	defer ticker.Stop()
	for {
		code, reader, err := integrationInfra.App.Exec(ctx, command)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, reader)
		require.NoError(t, err)
		if code == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("fixture command did not succeed: %v", command)
		}
	}
}

func toolMessages(t *testing.T, sessionID uuid.UUID) []api.Message {
	t.Helper()

	tools := make([]api.Message, 0)

	for _, message := range storedMessages(t, sessionID) {
		if message.Role == api.MessageRoleTool {
			tools = append(tools, message)
		}
	}

	return tools
}

func toolFailed(message api.Message) bool {
	return message.IsError != nil && *message.IsError
}
