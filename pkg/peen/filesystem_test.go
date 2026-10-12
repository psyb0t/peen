package peen_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/pkg/peen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootFilesystemMutationsAndEscapes(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	root, err := peen.RootFS(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll("nested", testDirectoryMode))
	require.NoError(t, root.CreateExclusive("nested/a", []byte("first"), testFileMode))
	require.ErrorIs(t, root.CreateExclusive("nested/a", []byte("overwrite"), testFileMode), fs.ErrExist)
	require.NoError(t, root.CreateExclusive("nested/b", []byte("second"), testFileMode))
	require.ErrorIs(t, root.RenameNoReplace("nested/a", "nested/b"), fs.ErrExist)
	require.NoError(t, root.WriteFileAtomic("nested/a", []byte("changed"), testFileMode))
	content, err := root.ReadFile("nested/a")
	require.NoError(t, err)
	assert.Equal(t, "changed", string(content))
	require.NoError(t, root.RenameNoReplace("nested/a", "nested/c"))
	require.NoError(t, root.Chmod("nested/c", testFileMode))
	require.NoError(t, root.Remove("nested/c"))
	require.NoError(t, root.RemoveAll("nested"))
	require.Error(t, root.RemoveAll("."))
	testCases := []struct{ name, path string }{
		{name: "parent traversal", path: "../escape"},
		{name: "absolute path", path: "/absolute"},
		{name: "nested escape", path: "nested/../../escape"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := root.ReadFile(tc.path)
			require.ErrorIs(t, err, fs.ErrInvalid)
			require.ErrorIs(t, root.CreateExclusive(tc.path, []byte("escape"), testFileMode), fs.ErrInvalid)
		})
	}
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, []byte("untouched"), testFileMode))
	require.NoError(t, os.Symlink(outside, filepath.Join(directory, "link")))
	_, err = root.ReadFile("link")
	require.Error(t, err)
	require.Error(t, root.WriteFileAtomic("link", []byte("bad"), testFileMode))
	content, err = os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, "untouched", string(content))
}

func TestVirtualFileToolsPreserveObservationAndModes(t *testing.T) {
	t.Parallel()
	const original = "original contents"
	const executableMode = fs.FileMode(0o700)
	testCases := []struct {
		name              string
		readFirst         bool
		changeBeforeWrite bool
		wantContent       string
		wantToolError     bool
	}{
		{name: "observed replacement", readFirst: true, wantContent: "replacement"},
		{name: "unobserved file", wantContent: original, wantToolError: true},
		{name: "stale observed file", readFirst: true, changeBeforeWrite: true, wantContent: "external change", wantToolError: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, err := peen.RootFS(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.CreateExclusive("note.txt", []byte(original), executableMode))
			var script []elelemtest.Turn
			if tc.readFirst {
				script = append(script, elelemtest.ToolCall("read", "readFile", `{"path":"note.txt"}`))
			}
			script = append(script,
				elelemtest.ToolCall("write", "writeFile", fmt.Sprintf(
					`{"path":"note.txt","content":"replacement","expectedSha256":"%x"}`,
					sha256.Sum256([]byte(original)),
				)),
				elelemtest.Text("done"),
			)
			options := validOptions(t)
			options.Filesystem = root
			options.WorkspaceID = "mutation-workspace"
			options.Models[testModelReference] = peen.ModelClient{
				Client: elelem.New(elelemtest.NewScriptedDriver(script...)),
				Model:  elelem.Model{ID: testModelID},
			}
			if tc.changeBeforeWrite {
				options.Hooks = map[string]peen.HookHandler{
					"preWriteFile": func(context.Context, peen.HookInvocation) (peen.HookOutcome, error) {
						return peen.HookOutcome{}, root.WriteFileAtomic("note.txt", []byte("external change"), executableMode)
					},
				}
			}
			runtime, err := peen.New(options)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
			_, err = runtime.Message(context.Background(), peen.MessageRequest{Message: "write"})
			require.NoError(t, err)
			data, err := root.ReadFile("note.txt")
			require.NoError(t, err)
			assert.Equal(t, tc.wantContent, string(data))
			info, err := root.Stat("note.txt")
			require.NoError(t, err)
			assert.Equal(t, executableMode, info.Mode().Perm())
			messages, err := runtime.ListMessages(context.Background(), peen.ListMessagesRequest{})
			require.NoError(t, err)
			var wrote bool
			for _, message := range messages.Items {
				if message.ToolCallID == "write" {
					wrote = true
					assert.Equal(t, tc.wantToolError, message.IsError)
				}
			}
			assert.True(t, wrote)
		})
	}
}

func TestReadOnlyFilesystem(t *testing.T) {
	t.Parallel()
	files, err := peen.FromIOFS(fstest.MapFS{
		"note.txt": {Data: []byte("virtual")},
	})
	require.NoError(t, err)
	content, err := files.ReadFile("note.txt")
	require.NoError(t, err)
	assert.Equal(t, "virtual", string(content))
	require.ErrorIs(t, files.WriteFileAtomic("note.txt", nil, testFileMode), fs.ErrPermission)
	require.ErrorIs(t, files.CreateExclusive("new.txt", nil, testFileMode), fs.ErrPermission)
	require.ErrorIs(t, files.RenameNoReplace("note.txt", "other"), fs.ErrPermission)
	_, err = files.ReadFile("../escape")
	require.Error(t, err)
}

func TestFromIOFSRejectsNilSources(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		source fs.FS
	}{
		{name: "nil interface"},
		{name: "nil pointer", source: (*fstest.MapFS)(nil)},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filesystem, err := peen.FromIOFS(tc.source)
			require.ErrorIs(t, err, fs.ErrInvalid)
			require.Nil(t, filesystem)
		})
	}
}

func TestVirtualRuntimeReadsWorkspaceAndHarness(t *testing.T) {
	t.Parallel()
	files, err := peen.FromIOFS(fstest.MapFS{
		"note.txt":         {Data: []byte("virtual contents")},
		"AGENTS.md":        {Data: []byte("Follow virtual workspace rules.")},
		"nested/AGENTS.md": {Data: []byte("Nested rules.")},
	})
	require.NoError(t, err)
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("read", "readFile", `{"path":"note.txt"}`),
		elelemtest.Text("done"),
	)
	options := validOptions(t)
	options.Models[testModelReference] = peen.ModelClient{
		Client: elelem.New(driver), Model: elelem.Model{ID: testModelID},
	}
	options.Filesystem = files
	options.WorkspaceID = "memory-workspace"
	options.InMemoryStore = true
	options.Harness = []peen.HarnessLayer{{Files: map[string]string{
		"AGENTS.md": "Follow memory layer rules.",
	}}}
	runtime, err := peen.New(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(context.Background())) })
	snapshot, err := runtime.Harness(context.Background(), options.WorkspaceID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(snapshot.Instructions), 3)
	result, err := runtime.Message(context.Background(), peen.MessageRequest{Message: "read"})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Message)
	page, err := runtime.ListMessages(context.Background(), peen.ListMessagesRequest{})
	require.NoError(t, err)
	var toolContent string
	for _, message := range page.Items {
		if message.Role == peen.MessageRoleTool {
			assert.False(t, message.IsError)
			toolContent = message.Content
		}
	}
	assert.Contains(t, toolContent, "virtual contents")
}
