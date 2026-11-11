package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compactToolTestHarness(t *testing.T, hook bool) json.RawMessage {
	t.Helper()
	config, workspace := t.TempDir(), t.TempDir()
	if hook {
		require.NoError(t, os.Mkdir(filepath.Join(config, ".agents"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(config, ".agents",
			"hooks.yaml"), []byte(`version: 1
postReadFile:
  - actions:
      - type: inject
        message: Hook ran.
`), 0o600))
	}

	resolver, err := harness.NewResolver(config, harness.Limits{})
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(workspace)
	require.NoError(t, err)
	checkpoint, err := snapshot.Checkpoint()
	require.NoError(t, err)

	return checkpoint
}

func TestCompactToolScheduleUsesSavedHarnessAndLegacyOptIn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		optIn, hook, customHook bool
		count                   int
	}{
		{name: "no hooks", optIn: true, count: 2},
		{name: "registered hook", optIn: true, hook: true, count: 5},
		{name: "legacy execution", count: 5},
		{name: "custom tool hook", optIn: true, customHook: true, count: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(
				elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
					runtimeToolArguments(t, runtimeToolFileName)),
				elelemtest.Text(runtimeToolFinalText))
			fixture := newExecutionLoopFixture(t, driver, true)
			var conversation workflowConversation
			require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
			conversation.Limits = &workflowExecutionLimits{CompactTools: tc.optIn}
			if tc.customHook {
				conversation.Limits.HookedTools = []string{toolNameReadFile}
			}
			conversation.Runtime = &workflowRuntimeState{
				Harness: compactToolTestHarness(t, tc.hook),
			}
			encoded, err := json.Marshal(conversation)
			require.NoError(t, err)
			fixture.input.Context = encoded
			phases := []workflowToolPhase{}
			fixture.engine.options.ToolPhase = func(_ context.Context,
				input workflowToolPhaseInput,
			) (workflowToolPhaseState, error) {
				phases = append(phases, input.Phase)
				state := input.State
				state.Outcome.Result.Content = "read"

				return state, nil
			}
			instance := startPhasedWorkflow(t, fixture)
			_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
				fixture.api, instance, workflowTestResultTimeout)
			require.NoError(t, err)
			assert.Len(t, phases, tc.count)
			if tc.count == 2 {
				assert.Equal(t, []workflowToolPhase{
					workflowToolPhaseHandler,
					workflowToolPhaseProject,
				}, phases)
			}

			page, err := fixture.base.store.ListExecutionActivities(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID,
				session.ListEventsOptions{})
			require.NoError(t, err)
			count := 0
			for _, activity := range page.Items {
				if activity.Name == toolPhaseActivityName {
					count++
				}
			}

			assert.Equal(t, tc.count, count)
		})
	}
}

func TestWorkflowLimitsRetainCustomToolHooks(t *testing.T) {
	t.Parallel()
	runtime := &Runtime{extraTools: []elelem.Tool{{
		Name:    "custom",
		PostRun: func(context.Context, *elelem.ToolEvent) error { return nil },
	}}}
	limits := runtime.workflowLimits(4)
	assert.Equal(t, []string{"custom"}, limits.HookedTools)
}
