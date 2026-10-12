package peen_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
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

func TestDirectoryHarnessPersistsAcrossRuntimeReopen(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		virtual bool
	}{
		{name: "host"},
		{name: "rooted virtual", virtual: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := validOptions(t)
			directory := t.TempDir()
			root, err := peen.RootFS(directory)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.MkdirAll("nested", testDirectoryMode))
			require.NoError(t, root.CreateExclusive("nested/AGENTS.md", []byte("nested-reopen-scope"), testFileMode))
			require.NoError(t, root.CreateExclusive("nested/file.txt", []byte("nested-reopen-file"), testFileMode))
			options.Workspace = directory
			if tc.virtual {
				options.Filesystem = root
				options.WorkspaceID = "directory-reopen"
				options.Workspace = ""
			}
			options.Harness = []peen.HarnessLayer{{Files: map[string]string{"AGENTS.md": "immutable layer"}}}
			driver := elelemtest.NewScriptedDriver(
				elelemtest.ToolCall("hop", "changeDirectory", `{"directory":"nested"}`),
				elelemtest.Text("first done"),
				elelemtest.ToolCall("read", "readFile", `{"path":"file.txt"}`),
				elelemtest.Text("second done"),
			)
			options.Models[testModelReference] = peen.ModelClient{Client: elelem.New(driver), Model: elelem.Model{ID: testModelID}}
			runtime, err := peen.New(options)
			require.NoError(t, err)
			ctx := context.Background()
			_, err = runtime.Message(ctx, peen.MessageRequest{Message: "hop", SystemPrompt: &peen.SystemPrompt{Content: "caller-base", Mode: peen.PromptModeReplace}})
			require.NoError(t, err)
			require.NoError(t, runtime.Close(ctx))
			runtime, err = peen.New(options)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, runtime.Close(ctx)) })
			_, err = runtime.Message(ctx, peen.MessageRequest{Message: "read"})
			require.NoError(t, err)
			requests := driver.Requests()
			require.Len(t, requests, 4)
			assert.Contains(t, requests[1].Messages[0].Text(), "caller-base")
			assert.Contains(t, requests[2].Messages[0].Text(), "nested-reopen-scope")
			assert.Contains(t, requests[2].Messages[0].Text(), "immutable layer")
			messages, err := runtime.ListMessages(ctx, peen.ListMessagesRequest{})
			require.NoError(t, err)
			var read bool
			for _, message := range messages.Items {
				if message.ToolCallID == "read" {
					read = true
					assert.False(t, message.IsError)
					assert.Contains(t, message.Content, "nested-reopen-file")
				}
			}
			assert.True(t, read)
		})
	}
}

func TestSameDirectoryHopRefreshesEditedInstructions(t *testing.T) {
	t.Parallel()
	options := validOptions(t)
	options.Workspace = t.TempDir()
	path := filepath.Join(options.Workspace, "AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte("old-scope"), testFileMode))
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("edit", "replaceInstructions", `{}`),
		elelemtest.ToolCall("refresh", "changeDirectory", `{"directory":"."}`),
		elelemtest.Text("done"),
	)
	options.Models[testModelReference] = peen.ModelClient{Client: elelem.New(driver), Model: elelem.Model{ID: testModelID}}
	options.Tools = []peen.Tool{{
		Name: "replaceInstructions", Description: "Replace fixture instructions.",
		ArgumentsSchema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler: func(context.Context, peen.ToolInput) (peen.ToolResult, error) {
			return peen.ToolResult{Content: "edited"}, os.WriteFile(path, []byte("new-scope"), testFileMode)
		},
	}}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	_, err = runtime.Message(context.Background(), peen.MessageRequest{Message: "refresh"})
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 3)
	assert.Contains(t, requests[0].Messages[0].Text(), "old-scope")
	assert.Contains(t, requests[2].Messages[0].Text(), "new-scope")
	assert.NotContains(t, requests[2].Messages[0].Text(), "old-scope")
}
