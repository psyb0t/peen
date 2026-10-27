package agent

import (
	"encoding/json"
	"testing"

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

// Parallel tools that read one path record the same revision. The merge
// settles the tie the same way whichever result arrives first, instead of
// failing the turn.
func TestMergeWorkflowRuntimeSettlesParallelReadsOfOnePath(t *testing.T) {
	t.Parallel()

	hashed := tools.ObservedPath{
		Path: "/same", Hash: "b", HasHash: true, Revision: 3,
	}
	unhashed := tools.ObservedPath{Path: "/same", Revision: 3}
	otherHash := tools.ObservedPath{
		Path: "/same", Hash: "a", HasHash: true, Revision: 3,
	}

	testCases := []struct {
		name          string
		first, second tools.ObservedPath
		want          tools.ObservedPath
	}{
		{
			name:  "a content read beats a search match",
			first: hashed, second: unhashed, want: hashed,
		},
		{
			name:  "a search match does not replace a content read",
			first: unhashed, second: hashed, want: hashed,
		},
		{
			name:  "two content reads keep the smaller hash",
			first: hashed, second: otherHash, want: otherHash,
		},
		{
			name:  "two content reads in the other order",
			first: otherHash, second: hashed, want: otherHash,
		},
		{
			name:  "identical reads",
			first: hashed, second: hashed, want: hashed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			merged, err := mergeWorkflowRuntime(
				&workflowRuntimeState{
					Observations: []tools.ObservedPath{tc.first},
				},
				&workflowRuntimeState{
					Observations: []tools.ObservedPath{tc.second},
				},
			)
			require.NoError(t, err)
			assert.Equal(t, []tools.ObservedPath{tc.want},
				merged.Observations)
		})
	}
}

// A round where readFile and searchText both touched one file completes.
func TestWorkflowMergeRoundAcceptsAReadAndASearchOfOneFile(t *testing.T) {
	t.Parallel()

	observations := [][]tools.ObservedPath{
		{{Path: "/plan.md", Hash: "content", HasHash: true, Revision: 1}},
		{{Path: "/plan.md", Revision: 1}},
	}
	results := make([]workflowToolResult, len(observations))

	for index, observed := range observations {
		encoded, err := json.Marshal(workflowConversation{
			Runtime: &workflowRuntimeState{Observations: observed},
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
	assert.Equal(t, observations[0], conversation.Runtime.Observations)
}

func TestMergeWorkflowRuntimeWithOneSideMissing(t *testing.T) {
	t.Parallel()

	state := &workflowRuntimeState{Observations: []tools.ObservedPath{
		{Path: "/same", Hash: "first", HasHash: true},
	}}

	merged, err := mergeWorkflowRuntime(state, nil)
	require.NoError(t, err)
	assert.Equal(t, state, merged)

	merged, err = mergeWorkflowRuntime(nil, state)
	require.NoError(t, err)
	assert.Equal(t, state, merged)
}
