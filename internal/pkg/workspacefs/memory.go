package workspacefs

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
)

const memoryFileMode = 0o444

// Memory holds immutable harness source files with synthetic parent
// directories.
type Memory struct{ entries map[string]memoryEntry }

type memoryEntry struct {
	name      string
	data      []byte
	directory bool
}

func (e memoryEntry) Name() string { return path.Base(e.name) }
func (e memoryEntry) Size() int64  { return int64(len(e.data)) }
func (e memoryEntry) Mode() fs.FileMode {
	if e.directory {
		return fs.ModeDir | memoryFileMode
	}

	return memoryFileMode
}
func (e memoryEntry) ModTime() time.Time { return time.Time{} }
func (e memoryEntry) IsDir() bool        { return e.directory }
func (e memoryEntry) Sys() any           { return nil }

// NewMemory copies valid paths and creates their implicit directories.
func NewMemory(files map[string]string) (*Memory, error) {
	memory := &Memory{entries: map[string]memoryEntry{
		".": {name: ".", directory: true},
	}}
	for name, data := range files {
		if _, err := ValidPath(name); err != nil {
			return nil, err
		}

		if name == "." || name == "" {
			return nil, ctxerrors.Wrap(
				fs.ErrInvalid,
				"memory root file",
			)
		}

		memory.entries[name] = memoryEntry{name: name, data: []byte(data)}
	}

	for name := range files {
		if err := memory.addParents(name); err != nil {
			return nil, err
		}
	}

	return memory, nil
}

func (m *Memory) addParents(name string) error {
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		entry, exists := m.entries[parent]
		if exists && !entry.directory {
			return ctxerrors.Wrap(fs.ErrInvalid, "memory parent is a file")
		}

		m.entries[parent] = memoryEntry{name: parent, directory: true}
	}

	return nil
}

func (m *Memory) Open(name string) (fs.File, error) {
	if _, err := ValidPath(name); err != nil {
		return nil, err
	}

	entry, exists := m.entries[name]
	if !exists {
		return nil, ctxerrors.Wrap(fs.ErrNotExist, "memory file missing")
	}

	file := &memoryFile{Reader: bytes.NewReader(entry.data), entry: entry}
	if entry.directory {
		for childName, child := range m.entries {
			if childName == name || path.Dir(childName) != name {
				continue
			}

			file.children = append(file.children, fs.FileInfoToDirEntry(child))
		}

		slices.SortFunc(file.children, func(a, b fs.DirEntry) int {
			return strings.Compare(a.Name(), b.Name())
		})
	}

	return file, nil
}

type memoryFile struct {
	*bytes.Reader
	entry    memoryEntry
	children []fs.DirEntry
	cursor   int
}

func (f *memoryFile) Stat() (fs.FileInfo, error) { return f.entry, nil }
func (f *memoryFile) Close() error               { return nil }
func (f *memoryFile) ReadDir(count int) ([]fs.DirEntry, error) {
	if !f.entry.directory {
		return nil, ctxerrors.Wrap(
			fs.ErrInvalid,
			"memory path is not directory",
		)
	}

	if count > 0 && f.cursor == len(f.children) {
		return nil, io.EOF
	}

	end := len(f.children)
	if count > 0 {
		end = min(end, f.cursor+count)
	}

	result := slices.Clone(f.children[f.cursor:end])
	f.cursor = end

	return result, nil
}
