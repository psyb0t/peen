package harness

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/psyb0t/peen/internal/pkg/workspacefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type malformedCanonicalReader struct {
	workspacefs.Reader
	canonical string
}

type plainVirtualFile struct {
	fs.File
}

type unreadableDirectoryReader struct {
	workspacefs.Reader
}

func (r unreadableDirectoryReader) Open(name string) (fs.File, error) {
	file, err := r.Reader.Open(name)
	if err != nil {
		return nil, err
	}

	return plainVirtualFile{File: file}, nil
}

func (unreadableDirectoryReader) ReadDir(string) ([]fs.DirEntry, error) {
	return nil, fs.ErrPermission
}

func (r malformedCanonicalReader) RealPath(string) (string, error) {
	return r.canonical, nil
}

func (r malformedCanonicalReader) Stat(string) (fs.FileInfo, error) {
	return r.Reader.Stat(".")
}

func TestCanonicalDirectoryRejectsMalformedVirtualAliases(t *testing.T) {
	t.Parallel()
	testCases := []struct{ name, canonical string }{
		{name: "absolute", canonical: "/absolute"},
		{name: "parent", canonical: ".."},
		{name: "escape", canonical: "../outside"},
		{name: "unclean", canonical: "a/../b"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := newResolutionState(Limits{})
			state.reader = malformedCanonicalReader{
				Reader:    workspacefs.ReadOnly{Source: fstest.MapFS{}},
				canonical: tc.canonical,
			}
			directory, err := state.canonicalDirectory("alias")
			require.ErrorIs(t, err, fs.ErrInvalid)
			assert.Empty(t, directory)
		})
	}
}

func TestRequireReadableLayerUsesReaderWhenOpenFileCannotReadDirectories(
	t *testing.T,
) {
	t.Parallel()
	state := newResolutionState(Limits{})
	state.reader = unreadableDirectoryReader{
		Reader: workspacefs.ReadOnly{Source: fstest.MapFS{}},
	}

	err := state.requireReadableLayer(".")
	require.ErrorIs(t, err, fs.ErrPermission)
}
