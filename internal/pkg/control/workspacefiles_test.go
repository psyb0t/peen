package control_test

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const workspaceFileTestLimit int64 = 4

type workspaceFileFixture struct {
	registry  *control.Registry
	sessionID uuid.UUID
	workspace string
	root      string
}

func newWorkspaceFileFixture(t *testing.T) workspaceFileFixture {
	t.Helper()
	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	root := canonicalTempDir(t)
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o750))
	policy, err := control.NewWorkspacePolicy([]string{root})
	require.NoError(t, err)
	agentSpaces, err := worker.NewAgentSpaceSet(
		[]worker.AgentSpace{{
			Name: worker.AgentSpaceControl,
			Kind: worker.KindControl,
		}},
		worker.AgentSpaceControl,
	)
	require.NoError(t, err)
	registry, err := control.NewRegistry(control.RegistryOptions{
		Store: store, Policy: policy, AgentSpaces: agentSpaces,
		RootAgent: testRootAgent, DefaultModel: testDefaultModel,
		MaxFileAPIBytes: workspaceFileTestLimit,
		SystemWorkspace: filepath.Join(root, "system-agent"),
	})
	require.NoError(t, err)
	opened, err := registry.Open(t.Context(), workspace, "")
	require.NoError(t, err)

	return workspaceFileFixture{
		registry: registry, sessionID: opened.Session.ID,
		workspace: workspace, root: root,
	}
}

func TestRegistryWorkspaceFileRoundTrip(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	content := base64.StdEncoding.EncodeToString([]byte("data"))
	entry, err := f.registry.WriteWorkspaceFile(
		t.Context(), f.sessionID, "nested/result.txt", content, true,
	)
	require.NoError(t, err)
	assert.EqualValues(t, "file", entry.Type)
	assert.EqualValues(t, 4, entry.Size)

	listing, err := f.registry.ListWorkspaceFiles(t.Context(), f.sessionID, "")
	require.NoError(t, err)
	assert.EqualValues(t, "directory", listing.Type)
	require.Len(t, listing.Entries, 1)
	assert.Equal(t, "nested", listing.Entries[0].Name)
	fileListing, err := f.registry.ListWorkspaceFiles(
		t.Context(), f.sessionID, "nested/result.txt",
	)
	require.NoError(t, err)
	assert.EqualValues(t, "file", fileListing.Type)
	assert.Empty(t, fileListing.Entries)
	read, err := f.registry.ReadWorkspaceFile(
		t.Context(), f.sessionID, "nested/result.txt",
	)
	require.NoError(t, err)
	assert.Equal(t, content, read.ContentBase64)
	assert.EqualValues(t, 4, read.Size)
	_, err = f.registry.WriteWorkspaceFile(
		t.Context(), f.sessionID, "nested/result.txt",
		base64.StdEncoding.EncodeToString([]byte("new!")), false,
	)
	require.NoError(t, err)
	require.NoError(t, f.registry.DeleteWorkspaceFile(
		t.Context(), f.sessionID, "nested/result.txt",
	))
	require.NoError(t, f.registry.DeleteWorkspaceFile(
		t.Context(), f.sessionID, "nested",
	))
}

func TestRegistryWorkspaceFileRejectsEscapes(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	paths := []string{
		"..", "../x", "/etc/passwd", "a/../b", "a//b", "bad\x00name",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			_, err := f.registry.ReadWorkspaceFile(t.Context(), f.sessionID, path)
			require.ErrorIs(t, err, commerr.ErrPermissionDenied)
		})
	}
}

func TestRegistryWorkspaceFileRejectsSymlinkEscape(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	outside := filepath.Join(f.root, "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.workspace, "link")))
	_, err := f.registry.ReadWorkspaceFile(t.Context(), f.sessionID, "link")
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	require.NoError(t, os.Symlink(f.root,
		filepath.Join(f.workspace, "outside")))
	_, err = f.registry.ListWorkspaceFiles(t.Context(), f.sessionID,
		"outside/outside.txt")
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	err = f.registry.DeleteWorkspaceFile(t.Context(), f.sessionID,
		"outside/outside.txt")
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestRegistryWorkspaceFileFollowsInternalRelativeSymlink(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	target := filepath.Join(f.workspace, "target.txt")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o600))
	require.NoError(t, os.Symlink(
		"target.txt",
		filepath.Join(f.workspace, "link"),
	))

	read, err := f.registry.ReadWorkspaceFile(t.Context(), f.sessionID, "link")
	require.NoError(t, err)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("old")),
		read.ContentBase64)

	_, err = f.registry.WriteWorkspaceFile(t.Context(), f.sessionID, "link",
		base64.StdEncoding.EncodeToString([]byte("new")), false)
	require.NoError(t, err)
	updated, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "new", string(updated))
	require.NoError(t, f.registry.DeleteWorkspaceFile(t.Context(), f.sessionID,
		"link"))
	assert.FileExists(t, target)
	require.NoError(t, os.Symlink("missing",
		filepath.Join(f.workspace, "dangling")))
	require.NoError(t, f.registry.DeleteWorkspaceFile(t.Context(), f.sessionID,
		"dangling"))
}

func TestRegistryWorkspaceFileRejectsSystemSession(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	system, err := f.registry.OpenSystemSession(t.Context())
	require.NoError(t, err)
	_, err = f.registry.ListWorkspaceFiles(t.Context(), system.Session.Id, "")
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestRegistryWorkspaceFileEnforcesSizeAndDeleteRules(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	_, err := f.registry.WriteWorkspaceFile(t.Context(), f.sessionID,
		"large", base64.StdEncoding.EncodeToString([]byte("12345")), false)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	largePath := filepath.Join(f.workspace, "large")
	require.NoError(t, os.WriteFile(largePath, []byte("12345"), 0o600))
	_, err = f.registry.ReadWorkspaceFile(t.Context(), f.sessionID, "large")
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	directory := filepath.Join(f.workspace, "nonempty")
	require.NoError(t, os.Mkdir(directory, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "child"),
		[]byte("x"), 0o600))
	err = f.registry.DeleteWorkspaceFile(t.Context(), f.sessionID, "nonempty")
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.FileExists(t, filepath.Join(directory, "child"))
}

func TestRegistryWorkspaceFileRejectsBadBase64AndMissingParents(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	_, err := f.registry.WriteWorkspaceFile(t.Context(), f.sessionID,
		"bad-base64", "***", false)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)

	_, err = f.registry.WriteWorkspaceFile(t.Context(), f.sessionID,
		"missing/child", base64.StdEncoding.EncodeToString([]byte("x")), false)
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestRegistryWorkspaceFileRefusesDirectoryAndFIFOReads(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	require.NoError(t, os.Mkdir(filepath.Join(f.workspace, "directory"), 0o750))
	require.NoError(t, syscall.Mkfifo(filepath.Join(f.workspace, "pipe"), 0o600))

	for _, name := range []string{"directory", "pipe"} {
		t.Run(name, func(t *testing.T) {
			_, err := f.registry.ReadWorkspaceFile(t.Context(), f.sessionID, name)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestRegistryWorkspaceFileHonorsCancelledContext(t *testing.T) {
	f := newWorkspaceFileFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.registry.ListWorkspaceFiles(ctx, f.sessionID, "")
	require.ErrorIs(t, err, context.Canceled)
	err = f.registry.DeleteWorkspaceFile(ctx, f.sessionID, "missing")
	require.ErrorIs(t, err, context.Canceled)
}
