package mcpserver

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/stretchr/testify/assert"
)

func (f *testBoundary) ListWorkspaceFiles(
	_ context.Context, _ uuid.UUID, path string,
) (control.WorkspaceFileListing, error) {
	f.accesses.Add(1)

	return control.WorkspaceFileListing{Path: path}, nil
}

func (f *testBoundary) ReadWorkspaceFile(
	_ context.Context, _ uuid.UUID, path string,
) (control.WorkspaceFileContent, error) {
	f.accesses.Add(1)

	return control.WorkspaceFileContent{Path: path, ContentBase64: "aGk="}, nil
}

func (f *testBoundary) WriteWorkspaceFile(
	_ context.Context, _ uuid.UUID, path, content string, _ bool,
) (control.WorkspaceFileEntry, error) {
	f.accesses.Add(1)

	return control.WorkspaceFileEntry{
		Name: path, Size: int64(len(content)),
	}, nil
}

func (f *testBoundary) DeleteWorkspaceFile(
	_ context.Context, _ uuid.UUID, _ string,
) error {
	f.accesses.Add(1)

	return nil
}

func TestFileTools(t *testing.T) {
	t.Parallel()
	client := testClient(t, (&testBoundary{}).dependencies())
	args := map[string]any{"sessionId": uuid.NewString(), "path": "file.txt"}
	listing := decodeResult[control.WorkspaceFileListing](t,
		callTool(t, client, toolListFiles, args))
	assert.Equal(t, "file.txt", listing.Path)
	content := decodeResult[control.WorkspaceFileContent](t,
		callTool(t, client, toolReadFile, args))
	assert.Equal(t, "aGk=", content.ContentBase64)
	args["contentBase64"] = "aGk="
	args["createDirectories"] = true
	entry := decodeResult[control.WorkspaceFileEntry](t,
		callTool(t, client, toolWriteFile, args))
	assert.Equal(t, "file.txt", entry.Name)
	delete(args, "contentBase64")
	delete(args, "createDirectories")
	deleted := decodeResult[deleteResult](t,
		callTool(t, client, toolDeleteFile, args))
	assert.True(t, deleted.Deleted)
}
