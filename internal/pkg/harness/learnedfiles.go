package harness

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// LearnedScope says which learned directory a learned file lives in.
type LearnedScope string

const (
	// LearnedScopeWorkspace is a workspace's own .agents/learned.
	LearnedScopeWorkspace LearnedScope = "workspace"
	// LearnedScopeGlobal is the global learned directory every session reads.
	LearnedScopeGlobal LearnedScope = "global"
)

// LearnedKind says whether a learned file is a rule or a skill.
type LearnedKind string

const (
	LearnedKindRule  LearnedKind = "rule"
	LearnedKindSkill LearnedKind = "skill"
)

const learnedNameSeparator = "/"

// LearnedDirectories are the two places learned files live for one session.
type LearnedDirectories struct {
	// Workspace is the session's workspace root. Its learned files are under
	// .agents/learned.
	Workspace string
	// Global is the global learned directory itself, or empty when the
	// deployment has none.
	Global string
}

// LearnedFileReference names one learned rule or skill.
type LearnedFileReference struct {
	Scope LearnedScope
	Kind  LearnedKind
	// Name is a rule's path under rules/ without .md, slash-separated, or a
	// skill's directory name.
	Name string
}

// LearnedFile is one learned rule, or one learned skill's SKILL.md.
type LearnedFile struct {
	LearnedFileReference

	Path       string
	SizeBytes  int64
	ModifiedAt time.Time
	// AlwaysLoaded is true for a rule without a scope, which is part of every
	// system prompt.
	AlwaysLoaded    bool
	EstimatedTokens int
}

// TokenEstimator estimates the tokens in a text.
type TokenEstimator func(text string) (int, error)

// ListLearnedFiles lists the learned rules and skills in both directories,
// workspace first, each sorted by kind and name. A missing directory lists
// nothing, and symbolic links are skipped as the resolver skips them.
func ListLearnedFiles(
	directories LearnedDirectories,
	estimate TokenEstimator,
) ([]LearnedFile, error) {
	files := make([]LearnedFile, 0)

	for _, scope := range []LearnedScope{
		LearnedScopeWorkspace,
		LearnedScopeGlobal,
	} {
		root := directories.root(scope)
		if root == "" {
			continue
		}

		scoped, err := listLearnedRoot(root, scope, estimate)
		if err != nil {
			return nil, err
		}

		files = append(files, scoped...)
	}

	return files, nil
}

// ReadLearnedFile returns one learned file and its content. A reference that
// names nothing, or a path that leaves its learned directory through a
// symbolic link, is commerr.ErrNotFound.
func ReadLearnedFile(
	directories LearnedDirectories,
	reference LearnedFileReference,
	estimate TokenEstimator,
) (LearnedFile, string, error) {
	path, _, err := directories.resolve(reference)
	if err != nil {
		return LearnedFile{}, "", err
	}

	file, content, err := describeLearnedFile(reference, path, estimate)
	if err != nil {
		return LearnedFile{}, "", err
	}

	return file, content, nil
}

// DeleteLearnedFile removes a learned rule's file or a learned skill's
// directory.
func DeleteLearnedFile(
	directories LearnedDirectories,
	reference LearnedFileReference,
) error {
	path, removable, err := directories.resolve(reference)
	if err != nil {
		return err
	}

	if reference.Kind == LearnedKindRule {
		if err := os.Remove(path); err != nil {
			return ctxerrors.Wrap(err, "remove learned rule")
		}

		return nil
	}

	if err := os.RemoveAll(removable); err != nil {
		return ctxerrors.Wrap(err, "remove learned skill")
	}

	return nil
}

func (d LearnedDirectories) root(scope LearnedScope) string {
	switch scope {
	case LearnedScopeWorkspace:
		if d.Workspace == "" {
			return ""
		}

		return filepath.Join(
			d.Workspace,
			agentsDirectoryName,
			learnedDirectoryName,
		)
	case LearnedScopeGlobal:
		return d.Global
	default:
		return ""
	}
}

// resolve turns a reference into the file it names and the path a delete
// removes: the rule file, or the skill's directory. The file must be a
// regular file reached without leaving the learned directory.
func (d LearnedDirectories) resolve(
	reference LearnedFileReference,
) (string, string, error) {
	segments, err := learnedNameSegments(reference)
	if err != nil {
		return "", "", err
	}

	root := d.root(reference.Scope)
	if root == "" {
		return "", "", ctxerrors.Wrapf(
			commerr.ErrNotFound,
			"no %s learned directory",
			reference.Scope,
		)
	}

	var relativeFile, relativeRemovable string

	switch reference.Kind {
	case LearnedKindRule:
		relativeFile = filepath.Join(rulesDirectoryName,
			filepath.Join(segments...)+agentsFileExtension)
		relativeRemovable = relativeFile
	case LearnedKindSkill:
		relativeRemovable = filepath.Join(skillsDirectoryName, segments[0])
		relativeFile = filepath.Join(relativeRemovable, skillFileName)
	default:
		return "", "", ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"unknown learned file kind",
		)
	}

	if err := requireContainedRegularFile(root, relativeFile); err != nil {
		return "", "", err
	}

	return filepath.Join(root, relativeFile),
		filepath.Join(root, relativeRemovable), nil
}

// learnedNameSegments validates a reference's name: a skill is one directory
// name, and a rule is up to maxRuleDirectoryDepth directories and a file name,
// none of them empty, "." or "..".
func learnedNameSegments(reference LearnedFileReference) ([]string, error) {
	if reference.Scope != LearnedScopeWorkspace &&
		reference.Scope != LearnedScopeGlobal {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"unknown learned scope",
		)
	}

	segments := strings.Split(reference.Name, learnedNameSeparator)

	maxSegments := 1
	if reference.Kind == LearnedKindRule {
		maxSegments = maxRuleDirectoryDepth + 1
	}

	if len(segments) > maxSegments {
		return nil, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"learned name is nested too deep",
		)
	}

	for _, segment := range segments {
		if segment == "" || segment == "." || segment == parentDirectoryName ||
			strings.ContainsAny(segment, `\`) {
			return nil, ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"invalid learned name",
			)
		}
	}

	return segments, nil
}

// requireContainedRegularFile refuses a path that does not exist, is not a
// regular file, or resolves outside root through a symbolic link.
func requireContainedRegularFile(root, relative string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return learnedLookupError(err)
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return learnedLookupError(err)
	}

	if resolved != filepath.Join(resolvedRoot, relative) {
		return ctxerrors.Wrap(
			commerr.ErrNotFound,
			"learned file leaves its directory",
		)
	}

	info, err := os.Lstat(resolved)
	if err != nil {
		return learnedLookupError(err)
	}

	if !info.Mode().IsRegular() {
		return ctxerrors.Wrap(commerr.ErrNotFound, "learned file is not a file")
	}

	return nil
}

func learnedLookupError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ctxerrors.Wrap(commerr.ErrNotFound, "learned file")
	}

	return ctxerrors.Wrap(err, "look up learned file")
}

func listLearnedRoot(
	root string,
	scope LearnedScope,
	estimate TokenEstimator,
) ([]LearnedFile, error) {
	rules, err := listLearnedRules(root, scope, estimate)
	if err != nil {
		return nil, err
	}

	skills, err := listLearnedSkills(root, scope, estimate)
	if err != nil {
		return nil, err
	}

	files := slices.Concat(rules, skills)
	slices.SortStableFunc(files, func(a, b LearnedFile) int {
		return cmp.Or(
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Name, b.Name),
		)
	})

	return files, nil
}

// learnedRuleWalk collects the rule files under one learned rules directory.
type learnedRuleWalk struct {
	rulesRoot string
	scope     LearnedScope
	estimate  TokenEstimator
	files     []LearnedFile
}

func listLearnedRules(
	root string,
	scope LearnedScope,
	estimate TokenEstimator,
) ([]LearnedFile, error) {
	walk := &learnedRuleWalk{
		rulesRoot: filepath.Join(root, rulesDirectoryName),
		scope:     scope,
		estimate:  estimate,
		files:     make([]LearnedFile, 0),
	}

	if err := filepath.WalkDir(walk.rulesRoot, walk.visit); err != nil {
		return nil, ctxerrors.Wrap(err, "list learned rules")
	}

	return walk.files, nil
}

func (w *learnedRuleWalk) visit(
	path string,
	entry fs.DirEntry,
	walkErr error,
) error {
	if walkErr != nil {
		return learnedWalkError(walkErr)
	}

	relative, err := filepath.Rel(w.rulesRoot, path)
	if err != nil {
		return ctxerrors.Wrap(err, "relate learned rule")
	}

	if entry.IsDir() {
		return learnedRuleDirectoryAction(relative)
	}

	if !entry.Type().IsRegular() ||
		filepath.Ext(path) != agentsFileExtension {
		return nil
	}

	file, _, err := describeLearnedFile(LearnedFileReference{
		Scope: w.scope,
		Kind:  LearnedKindRule,
		Name: filepath.ToSlash(
			strings.TrimSuffix(relative, agentsFileExtension),
		),
	}, path, w.estimate)
	if err != nil {
		return err
	}

	w.files = append(w.files, file)

	return nil
}

// learnedWalkError ignores a missing rules directory and wraps anything else.
func learnedWalkError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return ctxerrors.Wrap(err, "walk learned rules")
}

// learnedRuleDirectoryAction stops the walk below the deepest rule directory
// the resolver reads.
func learnedRuleDirectoryAction(relative string) error {
	if relative == "." {
		return nil
	}

	depth := strings.Count(relative, string(filepath.Separator)) + 1
	if depth > maxRuleDirectoryDepth {
		return filepath.SkipDir
	}

	return nil
}

func listLearnedSkills(
	root string,
	scope LearnedScope,
	estimate TokenEstimator,
) ([]LearnedFile, error) {
	skillsRoot := filepath.Join(root, skillsDirectoryName)

	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}

		return nil, ctxerrors.Wrap(err, "read learned skills")
	}

	files := make([]LearnedFile, 0, len(entries))

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		path := filepath.Join(skillsRoot, entry.Name(), skillFileName)

		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}

		file, _, err := describeLearnedFile(LearnedFileReference{
			Scope: scope,
			Kind:  LearnedKindSkill,
			Name:  entry.Name(),
		}, path, estimate)
		if err != nil {
			return nil, err
		}

		files = append(files, file)
	}

	return files, nil
}

// describeLearnedFile reads one learned file. A file over the harness file
// limit is described with no content and no token estimate, because the
// resolver skips it too.
func describeLearnedFile(
	reference LearnedFileReference,
	path string,
	estimate TokenEstimator,
) (LearnedFile, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return LearnedFile{}, "", learnedLookupError(err)
	}

	file := LearnedFile{
		LearnedFileReference: reference,
		Path:                 path,
		SizeBytes:            info.Size(),
		ModifiedAt:           info.ModTime().UTC(),
	}

	if info.Size() > defaultMaxFileBytes {
		return file, "", nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return LearnedFile{}, "", ctxerrors.Wrap(err, "read learned file")
	}

	content := string(raw)

	file.EstimatedTokens, err = estimate(content)
	if err != nil {
		return LearnedFile{}, "", ctxerrors.Wrap(err,
			"estimate learned file tokens")
	}

	if reference.Kind == LearnedKindRule {
		// A frontmatter warning is the resolver's to report and does not
		// change the scope it uses.
		document, _ := parseRuleDocument(content)
		file.AlwaysLoaded = len(document.paths) == 0
	}

	return file, content, nil
}
