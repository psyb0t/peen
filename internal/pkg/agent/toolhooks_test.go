package agent

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
