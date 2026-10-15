package tools

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobOwnerViewsPreserveProcessAttribution(t *testing.T) {
	t.Parallel()
	root := newTestJobExecutor(t)
	firstID, secondID := uuid.New(), uuid.New()
	first := root.ForAgentRun(firstID)
	second := root.ForAgentRun(secondID)
	changed, err := first.AtDirectory(t.Context(), ".")
	require.NoError(t, err)
	fork, err := first.Fork(t.Context())
	require.NoError(t, err)
	testCases := []struct {
		name     string
		executor *JobExecutor
		owner    uuid.UUID
	}{
		{name: "root", executor: root},
		{name: "child", executor: first, owner: firstID},
		{name: "sibling", executor: second, owner: secondID},
		{name: "directory", executor: changed, owner: firstID},
		{name: "fork", executor: fork, owner: firstID},
		{name: "grandchild", executor: fork.ForAgentRun(secondID), owner: secondID},
		{name: "root view", executor: first.ForAgentRun(uuid.Nil)},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			output, err := tc.executor.RunCommand(t.Context(), RunCommandInput{
				Command: "printf owner", Purpose: "test process owner",
			})
			require.NoError(t, err)
			assert.Equal(t, "owner", output.Stdout)
			job, found := root.jobs.Get(output.JobID)
			require.True(t, found)
			assert.Equal(t, tc.owner, job.Snapshot().AgentRunID)
		})
	}
}
