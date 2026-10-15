package agent

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowMergeRoundRetainsEveryToolObservation(t *testing.T) {
	t.Parallel()
	results := make([]workflowToolResult, 2)
	for index, path := range []string{"/first", "/second"} {
		encoded, err := json.Marshal(workflowConversation{
			Runtime: &workflowRuntimeState{
				Observations: []tools.ObservedPath{{Path: path}},
			},
		})
		require.NoError(t, err)
		results[index].Context = encoded
	}
	merged, err := workflowMergeRound(json.RawMessage(`{}`),
		make([]workflowToolInput, len(results)), results)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(merged, &conversation))
	require.NotNil(t, conversation.Runtime)
	assert.Equal(t, []tools.ObservedPath{
		{Path: "/first"}, {Path: "/second"},
	}, conversation.Runtime.Observations)
}

func TestWorkflowDirectoryChangesResetSkills(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		tool      string
		isError   bool
		wantNames []string
	}{
		{
			name: "successful hop", tool: toolNameChangeDirectory,
			wantNames: []string{"new"},
		},
		{
			name: "failed hop", tool: toolNameChangeDirectory, isError: true,
			wantNames: []string{"new", "old"},
		},
		{
			name: "ordinary tool", tool: toolNameUseSkill,
			wantNames: []string{"new", "old"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			current := &workflowRuntimeState{
				Directory: directoryMarker{Directory: "/same"},
				Skills:    &workflowSkillState{Delivered: []string{"old"}},
			}
			next := &workflowRuntimeState{
				Directory: current.Directory,
				Skills:    &workflowSkillState{Delivered: []string{"new"}},
			}
			resetWorkflowDirectorySkills(current, workflowToolInput{
				Call: elelem.ToolCall{Name: tc.tool},
			}, workflowToolResult{
				Result: elelem.ToolResult{IsError: tc.isError},
			})
			merged, err := mergeWorkflowRuntime(current, next)
			require.NoError(t, err)
			assert.Equal(t, tc.wantNames, merged.Skills.Delivered)
		})
	}
}

func TestWorkflowRuntimeHarnessChangeDropsPreviousSkills(t *testing.T) {
	t.Parallel()
	current := &workflowRuntimeState{
		Directory: directoryMarker{Directory: "/same", ContextHash: "old"},
		Skills:    &workflowSkillState{Delivered: []string{"old"}},
	}
	next := &workflowRuntimeState{
		Directory: directoryMarker{Directory: "/same", ContextHash: "new"},
		Skills:    &workflowSkillState{Delivered: []string{"new"}},
	}
	merged, err := mergeWorkflowRuntime(current, next)
	require.NoError(t, err)
	assert.Equal(t, []string{"new"}, merged.Skills.Delivered)
	resetWorkflowDirectorySkills(nil, workflowToolInput{},
		workflowToolResult{})
}

func TestMergeWorkflowRuntimeUsesReadOrderInsteadOfCallOrder(t *testing.T) {
	t.Parallel()
	newer := &workflowRuntimeState{Observations: []tools.ObservedPath{
		{Path: "/same", Hash: "new", HasHash: true, Revision: 2},
	}}
	older := &workflowRuntimeState{Observations: []tools.ObservedPath{
		{Path: "/same", Hash: "old", HasHash: true, Revision: 1},
	}}
	testCases := []struct {
		name          string
		first, second *workflowRuntimeState
	}{
		{name: "latest finishes first", first: newer, second: older},
		{name: "latest finishes last", first: older, second: newer},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			merged, err := mergeWorkflowRuntime(tc.first, tc.second)
			require.NoError(t, err)
			assert.Equal(t, newer.Observations, merged.Observations)
			merged.Observations[0].Hash = "modified"
			assert.Equal(t, "new", newer.Observations[0].Hash)
		})
	}
}

func TestMergeWorkflowRuntimeRefusesAmbiguousReadOrder(t *testing.T) {
	t.Parallel()
	first := &workflowRuntimeState{Observations: []tools.ObservedPath{
		{Path: "/same", Hash: "first", HasHash: true},
	}}
	second := &workflowRuntimeState{Observations: []tools.ObservedPath{
		{Path: "/same", Hash: "second", HasHash: true},
	}}
	_, err := mergeWorkflowRuntime(first, second)
	require.ErrorIs(t, err, commerr.ErrConflict)
	merged, err := mergeWorkflowRuntime(first, nil)
	require.NoError(t, err)
	assert.Equal(t, first, merged)
	merged, err = mergeWorkflowRuntime(nil, second)
	require.NoError(t, err)
	assert.Equal(t, second, merged)
}
