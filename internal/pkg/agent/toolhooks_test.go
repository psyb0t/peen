package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	hookInjectTestCallID  = "call-1"
	hookInjectTestNote    = "check the diff"
	hookInjectTestFile    = "readable.txt"
	hookInjectTestSession = "<events>\nreport\n</events>"
)

func TestMessageInjectionKeepsHookContextAndSessionEventsSeparate(
	t *testing.T,
) {
	t.Parallel()

	block, err := renderHookContext(
		harness.HookEventPostToolUse,
		"readFile",
		[]string{hookInjectTestNote},
	)
	require.NoError(t, err)

	runtime := &toolHookRuntime{
		messages: map[string][]string{},
		sessionEvents: func(
			context.Context,
			*elelem.ToolEvent,
		) (*elelem.MessageInjection, error) {
			return &elelem.MessageInjection{
				Type:    elelem.RoleUser,
				Content: hookInjectTestSession,
			}, nil
		},
	}
	runtime.add(hookInjectTestCallID, []string{block})

	injection, err := runtime.messageInjection(
		context.Background(),
		&elelem.ToolEvent{CallID: hookInjectTestCallID},
	)
	require.NoError(t, err)
	require.NotNil(t, injection)

	assert.Equal(t, elelem.RoleUser, injection.Type)
	assert.Equal(
		t,
		"<hook>\n<event>postToolUse</event>\n<tool>readFile</tool>\n\n"+
			hookInjectTestNote+"\n</hook>\n\n"+hookInjectTestSession,
		injection.Content,
	)
}

func TestMessageInjectionWithOnlyHookContext(t *testing.T) {
	t.Parallel()

	block, err := renderHookContext(
		harness.HookEventPostToolUse,
		"readFile",
		[]string{hookInjectTestNote},
	)
	require.NoError(t, err)

	runtime := &toolHookRuntime{messages: map[string][]string{}}
	runtime.add(hookInjectTestCallID, []string{block})

	injection, err := runtime.messageInjection(
		context.Background(),
		&elelem.ToolEvent{CallID: hookInjectTestCallID},
	)
	require.NoError(t, err)
	require.NotNil(t, injection)
	assert.Equal(t, block, injection.Content)
}

func TestAppendHookContextWrapsPreUserMessageInjections(t *testing.T) {
	t.Parallel()

	got, err := appendHookContext(
		"system prompt",
		harness.HookEventPreUserMessage,
		[]string{hookInjectTestNote},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		"system prompt"+systemSectionGap+
			"<hook>\n<event>preUserMessage</event>\n\n"+
			hookInjectTestNote+"\n</hook>",
		got,
	)

	unchanged, err := appendHookContext(
		"system prompt",
		harness.HookEventPreUserMessage,
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, "system prompt", unchanged)
}

func TestToolEventInjectionReachesTheModelAsHookContext(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			hookInjectTestCallID,
			toolNameReadFile,
			runtimeToolArguments(t, hookInjectTestFile),
		),
		elelemtest.Text("done"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, hookInjectTestFile),
		"content",
	)
	writeChildHarnessHooks(t, fixture, `version: 1
postReadFile:
  - name: note
    actions:
      - type: inject
        message: `+hookInjectTestNote+`
`)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "read it",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	requests := driver.Requests()
	require.Len(t, requests, 2)

	messages := requests[1].Messages
	assert.Equal(
		t,
		"<hook>\n<event>postReadFile</event>\n<tool>readFile</tool>\n\n"+
			hookInjectTestNote+"\n</hook>",
		messages[len(messages)-1].Text(),
	)
}

func TestSessionStartInjectionReachesTheModelAsHookContext(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	writeChildHarnessHooks(t, fixture, `version: 1
sessionStart:
  - name: note
    actions:
      - type: inject
        message: `+hookInjectTestNote+`
`)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "hello",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	requests := driver.Requests()
	require.Len(t, requests, 1)
	assert.Contains(
		t,
		requests[0].Messages[0].Text(),
		"<hook>\n<event>sessionStart</event>\n\n"+
			hookInjectTestNote+"\n</hook>",
	)
	assert.NotContains(
		t,
		requests[0].Messages[0].Text(),
		"Configured hook context",
	)
}

// Each tool maps its three lifecycle phases onto its own harness events. A
// mapper that returned another tool's event, or the same event for two phases,
// would fire the wrong workspace hook, so every pair is pinned here rather than
// spot-checked.
func TestToolHookEventsMapEveryPhaseToItsOwnEvent(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		mapper  func(hookEventPhase) (harness.HookEvent, bool)
		pre     harness.HookEvent
		success harness.HookEvent
		failure harness.HookEvent
	}{
		{
			name:    "read file",
			mapper:  readFileHookEvent,
			pre:     harness.HookEventPreReadFile,
			success: harness.HookEventPostReadFile,
			failure: harness.HookEventReadFileFailure,
		},
		{
			name:    "list files",
			mapper:  listFilesHookEvent,
			pre:     harness.HookEventPreListFiles,
			success: harness.HookEventPostListFiles,
			failure: harness.HookEventListFilesFailure,
		},
		{
			name:    "search text",
			mapper:  searchTextHookEvent,
			pre:     harness.HookEventPreSearchText,
			success: harness.HookEventPostSearchText,
			failure: harness.HookEventSearchTextFailure,
		},
		{
			name:    "write file",
			mapper:  writeFileHookEvent,
			pre:     harness.HookEventPreWriteFile,
			success: harness.HookEventPostWriteFile,
			failure: harness.HookEventWriteFileFailure,
		},
		{
			name:    "edit file",
			mapper:  editFileHookEvent,
			pre:     harness.HookEventPreEditFile,
			success: harness.HookEventPostEditFile,
			failure: harness.HookEventEditFileFailure,
		},
		{
			name:    "apply patch",
			mapper:  applyPatchHookEvent,
			pre:     harness.HookEventPreApplyPatch,
			success: harness.HookEventPostApplyPatch,
			failure: harness.HookEventApplyPatchFailed,
		},
		{
			name:    "move path",
			mapper:  movePathHookEvent,
			pre:     harness.HookEventPreMovePath,
			success: harness.HookEventPostMovePath,
			failure: harness.HookEventMovePathFailure,
		},
		{
			name:    "remove path",
			mapper:  removePathHookEvent,
			pre:     harness.HookEventPreRemovePath,
			success: harness.HookEventPostRemovePath,
			failure: harness.HookEventRemovePathFailed,
		},
		{
			name:    "make directory",
			mapper:  makeDirectoryHookEvent,
			pre:     harness.HookEventPreMakeDirectory,
			success: harness.HookEventPostMakeDirectory,
			failure: harness.HookEventMakeDirectoryFailure,
		},
	}

	seen := map[harness.HookEvent]string{}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for phase, want := range map[hookEventPhase]harness.HookEvent{
				hookEventPre:     tc.pre,
				hookEventSuccess: tc.success,
				hookEventFailure: tc.failure,
			} {
				got, ok := tc.mapper(phase)
				require.True(t, ok, "%s has a %s event", tc.name, phase)
				assert.Equal(t, want, got)
			}

			// An unknown phase reports no event rather than defaulting to one,
			// so a new phase cannot silently fire an existing hook.
			got, ok := tc.mapper(hookEventPhase("invented"))
			assert.False(t, ok)
			assert.Empty(t, got)
		})
	}

	// No two tools may share an event, or one tool's hook would run for
	// another tool's call.
	for _, tc := range testCases {
		for _, event := range []harness.HookEvent{
			tc.pre,
			tc.success,
			tc.failure,
		} {
			owner, taken := seen[event]
			require.False(
				t,
				taken,
				"%q is claimed by both %s and %s",
				event,
				owner,
				tc.name,
			)

			seen[event] = tc.name
		}
	}
}
