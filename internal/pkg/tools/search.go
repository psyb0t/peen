package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// lineMatcher reports whether one line satisfies the search pattern.
type lineMatcher func(line string) bool

// SearchText searches a file or a directory tree for lines matching
// Pattern, bounded by the executor's search limits. A directory is walked
// recursively without following symlinks: a symlinked entry keeps the type
// Lstat reports for it, so it is never treated as a regular file or
// descended into as a directory.
//
// Resolving and enumerating the target itself (a missing path, or a
// top-level directory that cannot be listed) is a hard error. A single
// candidate file that cannot be read, is too large, or is binary is skipped
// silently, whether that file is the target itself or was found while
// walking a directory.
func (e *Executor) SearchText(
	ctx context.Context,
	input SearchTextInput,
) (SearchTextOutput, error) {
	if err := ctx.Err(); err != nil {
		return SearchTextOutput{}, ctxerrors.Wrap(err, "search text canceled")
	}

	if input.Pattern == "" {
		return SearchTextOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"search pattern is required",
		)
	}

	matcher, err := buildLineMatcher(input)
	if err != nil {
		return SearchTextOutput{}, err
	}

	resolved, err := e.resolvePath(input.Path)
	if err != nil {
		return SearchTextOutput{}, ctxerrors.Wrap(err, "resolve search path")
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return SearchTextOutput{}, wrapPathError(err, "stat search path")
	}

	walker := &searchWalker{
		exec:    e,
		matcher: matcher,
		include: input.Include,
		limit:   e.resolveSearchLimit(input),
	}

	if info.IsDir() {
		err = walker.walkDir(ctx, resolved, true)
	} else {
		err = walker.searchNamedFile(ctx, resolved, info)
	}

	if err != nil {
		return SearchTextOutput{}, ctxerrors.Wrap(err, "search text")
	}

	sortSearchMatches(walker.matches)

	return SearchTextOutput{
		Matches:   walker.matches,
		Truncated: walker.truncated,
	}, nil
}

// buildLineMatcher compiles Pattern into a literal-substring or regular-
// expression line test, depending on Regex.
func buildLineMatcher(input SearchTextInput) (lineMatcher, error) {
	if !input.Regex {
		pattern := input.Pattern

		return func(line string) bool {
			return strings.Contains(line, pattern)
		}, nil
	}

	re, err := regexp.Compile(input.Pattern)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "compile search pattern")
	}

	return re.MatchString, nil
}

// resolveSearchLimit applies the match-count bound: zero or a value above
// the configured limit collapses onto that limit.
func (e *Executor) resolveSearchLimit(input SearchTextInput) int {
	bound := e.limits.MaxSearchMatches
	requested := input.MaxMatches

	if requested <= 0 || requested > bound {
		return bound
	}

	return requested
}

func sortSearchMatches(matches []SearchMatch) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}

		return matches[i].Line < matches[j].Line
	})
}

// searchWalker accumulates bounded matches across one file or directory
// tree.
type searchWalker struct {
	exec      *Executor
	matcher   lineMatcher
	include   []string
	limit     int
	matches   []SearchMatch
	truncated bool
}

func (w *searchWalker) walkDir(
	ctx context.Context,
	dirAbs string,
	isRoot bool,
) error {
	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "search walk canceled")
	}

	if w.truncated {
		return nil
	}

	dirEntries, err := os.ReadDir(dirAbs)
	if err != nil {
		if isRoot {
			return wrapPathError(err, "read search directory")
		}

		return nil
	}

	for _, dirEntry := range dirEntries {
		if w.truncated {
			return nil
		}

		if err := w.visit(ctx, dirAbs, dirEntry); err != nil {
			return err
		}
	}

	return nil
}

func (w *searchWalker) visit(
	ctx context.Context,
	dirAbs string,
	dirEntry os.DirEntry,
) error {
	info, err := dirEntry.Info()
	if err != nil {
		return nil //nolint:nilerr // A raced-away entry is skipped, not fatal.
	}

	absPath := filepath.Join(dirAbs, dirEntry.Name())

	if info.IsDir() {
		return w.walkDir(ctx, absPath, false)
	}

	if !info.Mode().IsRegular() {
		return nil
	}

	return w.processFile(ctx, absPath, info)
}

// searchNamedFile scans the single file the caller named. Unlike a file found
// while walking, an unreadable named file is an error: reporting zero matches
// would tell the caller the text is absent when it was never searched.
func (w *searchWalker) searchNamedFile(
	ctx context.Context,
	path string,
	info fs.FileInfo,
) error {
	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "search file canceled")
	}

	if !info.Mode().IsRegular() {
		return ctxerrors.Wrap(ErrNotRegularFile, path)
	}

	if info.Size() > w.exec.Limits().MaxSearchFileBytes {
		return ctxerrors.Wrap(ErrLimitExceeded, "file exceeds search bound")
	}

	content, _, err := readRegularFile(path, w.exec.Limits().MaxSearchFileBytes)
	if err != nil {
		return err
	}

	if isBinary(content) {
		return ctxerrors.Wrap(ErrBinaryContent, path)
	}

	if !matchesInclude(w.include, filepath.Base(path)) {
		return nil
	}

	w.collectMatches(path, content)
	w.exec.observe(path)

	return nil
}

// processFile scans one regular file for matches, skipping it silently when
// it fails the include filter, the size bound, a read, or the binary sniff.
// Silence is correct here: one bad file in a tree must not fail the search.
func (w *searchWalker) processFile(
	ctx context.Context,
	path string,
	info fs.FileInfo,
) error {
	if err := ctx.Err(); err != nil {
		return ctxerrors.Wrap(err, "search file canceled")
	}

	if !matchesInclude(w.include, filepath.Base(path)) {
		return nil
	}

	if info.Size() > w.exec.Limits().MaxSearchFileBytes {
		return nil
	}

	content, _, err := readRegularFile(path, w.exec.Limits().MaxSearchFileBytes)
	if err != nil {
		return nil //nolint:nilerr // One unreadable file is skipped, not fatal.
	}

	if isBinary(content) {
		return nil
	}

	w.collectMatches(path, content)

	return nil
}

func (w *searchWalker) collectMatches(path string, content []byte) {
	lines := splitLines(string(content))
	found := false

	for i, line := range lines {
		if w.truncated {
			break
		}

		if !w.matcher(line) {
			continue
		}

		w.matches = append(w.matches, SearchMatch{
			Path: path,
			Line: i + 1,
			Text: line,
		})

		found = true

		if len(w.matches) >= w.limit {
			w.truncated = true
		}
	}

	if found {
		w.exec.observe(path)
	}
}

// matchesInclude reports whether name matches at least one glob. No globs
// means every file matches. A malformed glob is treated as a non-match
// rather than failing the whole search.
func matchesInclude(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}

	for _, pattern := range patterns {
		ok, err := filepath.Match(pattern, name)
		if err == nil && ok {
			return true
		}
	}

	return false
}
