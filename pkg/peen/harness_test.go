package peen_test

import (
	"context"
	"testing"

	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryHarnessLayersOverrideAndRemainImmutable(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	first := map[string]string{
		".agents/agents/default.md": testAgentDocument,
	}
	const lastAgent = `---
name: default
description: memory agent
---
Use the final memory layer.`
	last := map[string]string{
		".agents/agents/default.md": lastAgent,
	}
	options.Harness = []peen.HarnessLayer{
		{Files: first}, {Files: last},
	}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(context.Background()))
	})
	last[".agents/agents/default.md"] = testAgentDocument
	delete(first, ".agents/agents/default.md")
	snapshot, err := runtime.Harness(context.Background(), options.Workspace)
	require.NoError(t, err)
	var selected *peen.HarnessAgent
	for index := range snapshot.Agents {
		if snapshot.Agents[index].Name == testRootAgent {
			selected = &snapshot.Agents[index]
		}
	}
	require.NotNil(t, selected)
	assert.Equal(t, "Use the final memory layer.", selected.Instructions)
	selected.Instructions = "mutated returned snapshot"
	next, err := runtime.Harness(context.Background(), options.Workspace)
	require.NoError(t, err)
	assert.Equal(t, snapshot.Hash, next.Hash)
	for _, item := range next.Agents {
		if item.Name == testRootAgent {
			assert.Equal(t, "Use the final memory layer.", item.Instructions)
		}
	}
}
