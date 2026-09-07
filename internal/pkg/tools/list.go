package tools

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/psyb0t/ctxerrors"
)

// rootListDepth is the depth assigned to the entries directly inside the
// listed directory. Non-recursive listings never go past this depth.
const rootListDepth = 1

// ListFiles lists the contents of one directory, bounded by the executor's
// list limits. An empty Path resolves to the workspace; any other Path is
// resolved per Executor.resolvePath, including outside the workspace.
func (e *Executor) ListFiles(
	ctx context.Context,
	input ListFilesInput,
) (ListFilesOutput, error) {
	if err := ctx.Err(); err != nil {
		return ListFilesOutput{}, ctxerrors.Wrap(err, "list files canceled")
	}

	resolved, err := e.resolvePath(input.Path)
	if err != nil {
		return ListFilesOutput{}, ctxerrors.Wrap(err, "resolve list path")
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return ListFilesOutput{}, wrapPathError(err, "stat list directory")
	}

	if !info.IsDir() {
		return ListFilesOutput{}, ctxerrors.Wrap(ErrNotDirectory, resolved)
	}

	walker := &listWalker{exec: e, maxDepth: e.resolveListDepth(input)}

	if err := walker.walk(ctx, resolved, "", rootListDepth, true); err != nil {
		return ListFilesOutput{}, ctxerrors.Wrap(err, "walk directory tree")
	}

	sort.Slice(walker.entries, func(i, j int) bool {
		return walker.entries[i].Path < walker.entries[j].Path
	})

	e.observe(resolved)

	return ListFilesOutput{
		Directory: resolved,
		Entries:   walker.entries,
		Truncated: walker.truncated,
	}, nil
}

// resolveListDepth applies the recursion bound: a non-recursive listing
// covers exactly one directory level, and a recursive MaxDepth of zero or
// above the configured limit collapses onto that limit.
func (e *Executor) resolveListDepth(input ListFilesInput) int {
	if !input.Recursive {
		return rootListDepth
	}

	limit := e.limits.MaxListDepth
	depth := input.MaxDepth

	if depth <= 0 || depth > limit {
		depth = limit
	}

	return depth
}

// listWalker accumulates a bounded, symlink-safe directory listing. A
// symlink is reported as an entry but never descended into, and an
// unreadable subdirectory is skipped rather than failing the whole walk.
type listWalker struct {
	exec      *Executor
	maxDepth  int
	entries   []FileEntry
	truncated bool
}

func (w *listWalker) walk(
	ctx context.Context,
	dirAbs, relPrefix string,
	depth int,
	isRoot bool,
) error {
	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "list walk canceled")
	}

	dirEntries, err := os.ReadDir(dirAbs)
	if err != nil {
		if isRoot {
			return wrapPathError(err, "read list directory")
		}

		return nil
	}

	for _, dirEntry := range dirEntries {
		if w.truncated {
			return nil
		}

		if err := w.visit(ctx, dirAbs, relPrefix, depth, dirEntry); err != nil {
			return err
		}
	}

	return nil
}

func (w *listWalker) visit(
	ctx context.Context,
	dirAbs, relPrefix string,
	depth int,
	dirEntry os.DirEntry,
) error {
	info, err := dirEntry.Info()
	if err != nil {
		return nil //nolint:nilerr // A raced-away entry is skipped, not fatal.
	}

	relPath := joinListPath(relPrefix, dirEntry.Name())
	absPath := filepath.Join(dirAbs, dirEntry.Name())
	entryType := entryTypeOf(info.Mode())

	w.entries = append(w.entries, FileEntry{
		Path: relPath,
		Type: entryType,
		Size: info.Size(),
		Mode: info.Mode().Perm().String(),
	})
	w.exec.observe(absPath)

	if len(w.entries) >= w.exec.Limits().MaxListEntries {
		w.truncated = true

		return nil
	}

	if entryType != EntryTypeDirectory || depth >= w.maxDepth {
		return nil
	}

	return w.walk(ctx, absPath, relPath, depth+1, false)
}

// joinListPath builds a forward-slash relative path without a leading `./`.
func joinListPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "/" + name
}
