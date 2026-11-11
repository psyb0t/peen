package agent

import (
	"encoding/json"
	"slices"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

// The saved opt-in keeps histories created before this schedule replayable.
// Tools that stage a directory or a passive wait retain separate phases.
func compactWorkflowTool(input workflowToolInput) bool {
	if input.Call.Name == toolNameAsk ||
		input.Call.Name == toolNameWaitMessages ||
		input.Call.Name == toolNameChangeDirectory {
		return false
	}

	snapshot, ok := compactToolSnapshot(input)
	if !ok {
		return false
	}

	return !matchingWorkflowToolHook(snapshot, input.Call.Name)
}

func compactToolSnapshot(input workflowToolInput) (harness.Snapshot, bool) {
	var conversation workflowConversation
	if json.Unmarshal(input.Round.Execution.Context, &conversation) != nil ||
		conversation.Limits == nil || !conversation.Limits.CompactTools ||
		conversation.Runtime == nil {
		return harness.Snapshot{}, false
	}

	if slices.Contains(conversation.Limits.HookedTools, input.Call.Name) {
		return harness.Snapshot{}, false
	}

	snapshot, err := harness.RestoreCheckpoint(conversation.Runtime.Harness)

	return snapshot, err == nil
}

func matchingWorkflowToolHook(snapshot harness.Snapshot, name string) bool {
	events := append(preHookEvents(name), successHookEvents(name)...)
	events = append(events, failureHookEvents(name)...)
	events = append(events, harness.HookEventPostToolUse)

	for _, hook := range snapshot.Hooks() {
		if !slices.Contains(events, hook.Event) {
			continue
		}

		if hook.Match.Tool == "" || hook.Match.Tool == name {
			return true
		}
	}

	return false
}

func inheritWorkflowToolPolicy(
	encoded json.RawMessage,
	conversation *workflowConversation,
) error {
	var saved workflowConversation
	if err := json.Unmarshal(encoded, &saved); err != nil {
		return ctxerrors.Wrap(err, "decode saved tool schedule")
	}

	conversation.Limits.CompactTools = false
	conversation.Limits.HookedTools = nil

	if saved.Limits != nil {
		conversation.Limits.CompactTools = saved.Limits.CompactTools
		conversation.Limits.HookedTools = saved.Limits.HookedTools
	}

	return nil
}
