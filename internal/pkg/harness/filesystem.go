package harness

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/workspacefs"
)

const (
	virtualHarnessRoot       = "."
	virtualWorkspacePriority = 1
	virtualNestedPriority    = 2
	virtualMemoryPriority    = 3
)

func (r Resolver) resolveVirtual(identity string) (Snapshot, error) {
	state := newResolutionState(r.limits)
	if err := state.discoverEmbedded(
		r.disabledSkills,
		r.disabledAgents,
	); err != nil {
		return Snapshot{}, err
	}

	if err := state.discoverLayer(r.configRoot, 0, true); err != nil {
		return Snapshot{}, err
	}

	state.reader = r.filesystem
	if err := state.discoverLayer(
		virtualHarnessRoot,
		virtualWorkspacePriority,
		false,
	); err != nil {
		return Snapshot{}, err
	}

	if err := state.discoverNestedInstructions(
		virtualHarnessRoot,
		r.configRoot,
		virtualNestedPriority,
	); err != nil {
		return Snapshot{}, err
	}

	if err := r.discoverMemoryLayers(state, virtualMemoryPriority); err != nil {
		return Snapshot{}, err
	}

	return state.snapshot(r.configRoot, identity)
}

func (r Resolver) discoverMemoryLayers(
	state *resolutionState,
	priority int,
) error {
	for index, reader := range r.memoryLayers {
		state.reader = reader
		if err := state.discoverLayer(
			virtualHarnessRoot,
			priority+index,
			false,
		); err != nil {
			return ctxerrors.Wrap(err, "discover memory harness layer")
		}
	}

	return nil
}

func (s *resolutionState) canonicalDirectory(name string) (string, error) {
	if s.reader == nil {
		return canonicalDirectory(name)
	}

	resolved, err := s.reader.RealPath(name)
	if err != nil {
		return "", ctxerrors.Wrap(err, "resolve virtual directory")
	}

	info, err := s.reader.Stat(resolved)
	if err != nil {
		return "", ctxerrors.Wrap(err, "stat virtual directory")
	}

	if !info.IsDir() {
		return "", ctxerrors.Wrap(ErrNotDirectory, name)
	}

	return resolved, nil
}

func (s *resolutionState) optionalCanonicalPath(
	name string,
) (string, bool, error) {
	if s.reader == nil {
		return optionalCanonicalPath(name)
	}

	if _, err := s.reader.Lstat(name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}

		return "", false, ctxerrors.Wrap(err, "inspect virtual path")
	}

	resolved, err := s.reader.RealPath(name)
	if err != nil {
		return "", false, ctxerrors.Wrap(err, "resolve virtual path")
	}

	return resolved, true, nil
}

func (s *resolutionState) optionalDirectory(name string) (string, bool, error) {
	if s.reader == nil {
		return optionalDirectory(name)
	}

	resolved, found, err := s.optionalCanonicalPath(name)
	if err != nil || !found {
		return resolved, found, err
	}

	info, err := s.reader.Stat(resolved)
	if err != nil {
		return "", false, ctxerrors.Wrap(err, "stat harness directory")
	}

	if !info.IsDir() {
		return "", false, ctxerrors.Wrap(ErrNotDirectory, name)
	}

	return resolved, true, nil
}

func (s *resolutionState) sortedDirectoryEntries(
	name string,
	maximum int,
) ([]fs.DirEntry, error) {
	if s.reader == nil {
		return sortedDirectoryEntries(name, maximum)
	}

	entries, err := s.reader.ReadDir(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read virtual entries")
	}

	if len(entries) > maximum {
		return nil, ctxerrors.Wrap(ErrResourceLimit, "directory entry bound")
	}

	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	return entries, nil
}

func (s *resolutionState) readRegularFileLimited(
	name string,
	maximum int64,
) ([]byte, error) {
	if s.reader == nil {
		return readRegularFileLimited(name, maximum)
	}

	file, err := s.reader.Open(name)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open virtual harness file")
	}

	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}

	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errors.Join(ctxerrors.Wrap(ErrResourceLimit,
			"bounded regular harness file required"), file.Close())
	}

	data, exceeded, readErr := readLimited(file, maximum)
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, ctxerrors.Wrap(err, "read virtual harness file")
	}

	if exceeded {
		return nil, ctxerrors.Wrap(
			ErrResourceLimit,
			"harness file grew beyond bound",
		)
	}

	return data, nil
}

func (s *resolutionState) walkDir(root string, visit fs.WalkDirFunc) error {
	var err error
	if s.reader == nil {
		err = filepath.WalkDir(root, visit)
	} else {
		err = workspacefs.WalkDir(s.reader, root, visit)
	}

	if err != nil {
		return ctxerrors.Wrap(err, "walk harness directory")
	}

	return nil
}
