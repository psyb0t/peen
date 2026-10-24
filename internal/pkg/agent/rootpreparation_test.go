package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootWorkflowPreparationLoadsDirectoryHopBeforeNextModel(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(target, "AGENTS.md"),
		[]byte("Target directory instructions."), runtimeTestFileMode))
	arguments, err := json.Marshal(changeDirectoryInput{Directory: target})
	require.NoError(t, err)
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("hopCall", toolNameChangeDirectory, string(arguments)),
		elelemtest.Text("finished in target"),
	)
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "finished in target", result.Text)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.NotContains(t, requests[0].Messages[0].Text(),
		"Target directory instructions.")
	assert.Contains(t, requests[1].Messages[0].Text(),
		"Target directory instructions.")
	assert.Equal(t, target, prepared.workspace)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &conversation))
	require.NotNil(t, conversation.Runtime)
	assert.Equal(t, target, conversation.Runtime.Directory.Directory)
	assert.NotEmpty(t, conversation.Runtime.Directory.ContextHash)
	assert.NotEmpty(t, conversation.Runtime.Directory.PromptHash)
	savedHarness, err := harness.RestoreCheckpoint(conversation.Runtime.Harness)
	require.NoError(t, err)
	assert.Equal(t, prepared.snapshot.Hash(), savedHarness.Hash())
	assert.Equal(t, target, savedHarness.Workspace())
	assert.Equal(t, prepared.snapshot.Instructions(), savedHarness.Instructions())
	messages, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, messages.Items, 4)
	assert.Equal(t, target, messages.Items[2].Workspace)
	fixture.stop()
}
