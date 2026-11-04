package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	learnedFilesTestNestedRule = "go/errors"
	learnedFilesTestTokens     = 7
)

func fixedTokenEstimate(string) (int, error) {
	return learnedFilesTestTokens, nil
}

// newLearnedFilesFixture writes an unscoped and a nested scoped rule and a
// skill in the workspace, and one rule in the global learned directory.
func newLearnedFilesFixture(t *testing.T) LearnedDirectories {
	t.Helper()

	workspace := t.TempDir()
	global := filepath.Join(t.TempDir(), "learned")

	workspaceLearned := learnedDirectory(workspace)
	writeLearnedRule(t, workspaceLearned, "style.md", learnedTestLearnedRule)
	writeLearnedRule(t, workspaceLearned,
		learnedFilesTestNestedRule+agentsFileExtension, learnedTestScopedRule)
	writeLearnedSkill(t, workspaceLearned)
	writeLearnedRule(t, global, "global.md", learnedTestGlobalRule)

	return LearnedDirectories{Workspace: workspace, Global: global}
}

func TestListLearnedFiles(t *testing.T) {
	t.Parallel()

	directories := newLearnedFilesFixture(t)

	files, err := ListLearnedFiles(directories, fixedTokenEstimate)
	require.NoError(t, err)

	references := make([]LearnedFileReference, 0, len(files))
	for _, file := range files {
		references = append(references, file.LearnedFileReference)
		assert.Equal(t, learnedFilesTestTokens, file.EstimatedTokens)
		assert.FileExists(t, file.Path)
	}

	assert.Equal(t, []LearnedFileReference{
		{
			Scope: LearnedScopeWorkspace,
			Kind:  LearnedKindRule,
			Name:  learnedFilesTestNestedRule,
		},
		{Scope: LearnedScopeWorkspace, Kind: LearnedKindRule, Name: "style"},
		{
			Scope: LearnedScopeWorkspace,
			Kind:  LearnedKindSkill,
			Name:  learnedTestSkillName,
		},
		{Scope: LearnedScopeGlobal, Kind: LearnedKindRule, Name: "global"},
	}, references)

	assert.False(t, files[0].AlwaysLoaded, "a scoped rule loads on demand")
	assert.True(t, files[1].AlwaysLoaded, "an unscoped rule loads every turn")
	assert.False(t, files[2].AlwaysLoaded, "a skill is never in the prompt")
}

// A missing learned directory lists nothing instead of failing the request.
func TestListLearnedFilesWithoutLearnedDirectories(t *testing.T) {
	t.Parallel()

	files, err := ListLearnedFiles(LearnedDirectories{
		Workspace: t.TempDir(),
		Global:    filepath.Join(t.TempDir(), "missing"),
	}, fixedTokenEstimate)
	require.NoError(t, err)
	assert.Empty(t, files)
}

// A symbolic link in a learned directory is skipped, as the resolver skips
// it, so the list never shows a file from somewhere else.
func TestListLearnedFilesSkipsSymbolicLinks(t *testing.T) {
	t.Parallel()

	directories := newLearnedFilesFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.md")
	writeFile(t, outside, "outside")
	require.NoError(t, os.Symlink(outside, filepath.Join(
		learnedDirectory(directories.Workspace),
		rulesDirectoryName,
		"linked.md",
	)))

	files, err := ListLearnedFiles(directories, fixedTokenEstimate)
	require.NoError(t, err)

	for _, file := range files {
		assert.NotEqual(t, "linked", file.Name)
	}
}

func TestReadLearnedFile(t *testing.T) {
	t.Parallel()

	directories := newLearnedFilesFixture(t)

	file, content, err := ReadLearnedFile(directories, LearnedFileReference{
		Scope: LearnedScopeGlobal,
		Kind:  LearnedKindRule,
		Name:  "global",
	}, fixedTokenEstimate)
	require.NoError(t, err)
	assert.Equal(t, learnedTestGlobalRule, content)
	assert.True(t, file.AlwaysLoaded)

	_, content, err = ReadLearnedFile(directories, LearnedFileReference{
		Scope: LearnedScopeWorkspace,
		Kind:  LearnedKindSkill,
		Name:  learnedTestSkillName,
	}, fixedTokenEstimate)
	require.NoError(t, err)
	assert.Contains(t, content, learnedTestLearnedSkill)
}

// Only a file under the named learned directory can be read or deleted. A
// name that climbs out is invalid, and a symbolic link that leads out is
// treated as missing.
func TestLearnedFileReferencesStayInsideTheirDirectory(t *testing.T) {
	t.Parallel()

	directories := newLearnedFilesFixture(t)
	outside := filepath.Join(directories.Workspace, "AGENTS.md")
	writeFile(t, outside, "user instructions")
	require.NoError(t, os.Symlink(outside, filepath.Join(
		learnedDirectory(directories.Workspace),
		rulesDirectoryName,
		"linked.md",
	)))

	testCases := []struct {
		name      string
		reference LearnedFileReference
		wantErr   error
	}{
		{
			name: "parent directory",
			reference: LearnedFileReference{
				Scope: LearnedScopeWorkspace,
				Kind:  LearnedKindRule,
				Name:  "../../AGENTS",
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "nested skill name",
			reference: LearnedFileReference{
				Scope: LearnedScopeWorkspace,
				Kind:  LearnedKindSkill,
				Name:  "a/b",
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "unknown scope",
			reference: LearnedFileReference{
				Scope: "elsewhere",
				Kind:  LearnedKindRule,
				Name:  "style",
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "symbolic link out",
			reference: LearnedFileReference{
				Scope: LearnedScopeWorkspace,
				Kind:  LearnedKindRule,
				Name:  "linked",
			},
			wantErr: commerr.ErrNotFound,
		},
		{
			name: "missing rule",
			reference: LearnedFileReference{
				Scope: LearnedScopeWorkspace,
				Kind:  LearnedKindRule,
				Name:  "missing",
			},
			wantErr: commerr.ErrNotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := ReadLearnedFile(directories, tc.reference,
				fixedTokenEstimate)
			require.ErrorIs(t, err, tc.wantErr)

			err = DeleteLearnedFile(directories, tc.reference)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}

	assert.FileExists(t, outside)
}

// Deleting a rule removes its file. Deleting a skill removes its whole
// directory, so the resolver stops finding it.
func TestDeleteLearnedFile(t *testing.T) {
	t.Parallel()

	directories := newLearnedFilesFixture(t)
	learned := learnedDirectory(directories.Workspace)

	require.NoError(t, DeleteLearnedFile(directories, LearnedFileReference{
		Scope: LearnedScopeWorkspace,
		Kind:  LearnedKindRule,
		Name:  learnedFilesTestNestedRule,
	}))
	assert.NoFileExists(t, filepath.Join(learned, rulesDirectoryName,
		learnedFilesTestNestedRule+agentsFileExtension))

	require.NoError(t, DeleteLearnedFile(directories, LearnedFileReference{
		Scope: LearnedScopeWorkspace,
		Kind:  LearnedKindSkill,
		Name:  learnedTestSkillName,
	}))
	assert.NoDirExists(t, filepath.Join(learned, skillsDirectoryName,
		learnedTestSkillName))

	files, err := ListLearnedFiles(directories, fixedTokenEstimate)
	require.NoError(t, err)
	assert.Len(t, files, 2)
}
