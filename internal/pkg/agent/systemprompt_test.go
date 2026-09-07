package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	systemPromptTestReplacement = "You are a narrowly scoped review agent."
	systemPromptTestAddition    = "Never touch files outside the review set."
	systemPromptTestFileMode    = 0o600
	systemPromptTestDirMode     = 0o750
)

func TestLoadSystemPrompt(t *testing.T) {
	t.Parallel()

	t.Run("no configuration directory uses the embedded default", func(t *testing.T) {
		t.Parallel()

		prompt, err := LoadSystemPrompt("")
		require.NoError(t, err)
		assert.Equal(t, defaultSystemPrompt, prompt)
	})

	t.Run("a directory with neither file uses the default", func(t *testing.T) {
		t.Parallel()

		prompt, err := LoadSystemPrompt(t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, defaultSystemPrompt, prompt)
	})

	t.Run("SYSTEM.md replaces the default entirely", func(t *testing.T) {
		t.Parallel()

		directory := t.TempDir()
		writePromptFile(
			t,
			directory,
			systemPromptFileName,
			"  "+systemPromptTestReplacement+"\n",
		)

		prompt, err := LoadSystemPrompt(directory)
		require.NoError(t, err)
		assert.Equal(t, systemPromptTestReplacement, prompt)
		assert.NotContains(t, prompt, defaultSystemPrompt)
	})

	t.Run("APPEND_SYSTEM.md extends the default", func(t *testing.T) {
		t.Parallel()

		directory := t.TempDir()
		writePromptFile(
			t,
			directory,
			appendSystemPromptFileName,
			systemPromptTestAddition,
		)

		prompt, err := LoadSystemPrompt(directory)
		require.NoError(t, err)
		assert.Contains(t, prompt, defaultSystemPrompt)
		assert.Contains(t, prompt, systemPromptTestAddition)
	})

	t.Run("both files compose in the documented order", func(t *testing.T) {
		t.Parallel()

		directory := t.TempDir()
		writePromptFile(
			t,
			directory,
			systemPromptFileName,
			systemPromptTestReplacement,
		)
		writePromptFile(
			t,
			directory,
			appendSystemPromptFileName,
			systemPromptTestAddition,
		)

		prompt, err := LoadSystemPrompt(directory)
		require.NoError(t, err)
		assert.Equal(
			t,
			systemPromptTestReplacement+
				systemSectionGap+
				systemPromptTestAddition,
			prompt,
		)
	})
}

func TestLoadSystemPromptRejectsUnusableFiles(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		file    string
		content string
		asDir   bool
	}{
		{
			name:    "an empty replacement",
			file:    systemPromptFileName,
			content: "   \n\t ",
		},
		{
			name:    "an empty append",
			file:    appendSystemPromptFileName,
			content: "\n",
		},
		{
			name:    "an oversized replacement",
			file:    systemPromptFileName,
			content: strings.Repeat("a", maxSystemPromptFileBytes+1),
		},
		{
			name:  "a directory in place of the file",
			file:  systemPromptFileName,
			asDir: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			if tc.asDir {
				require.NoError(t, os.MkdirAll(
					filepath.Join(directory, tc.file),
					systemPromptTestDirMode,
				))
			} else {
				writePromptFile(t, directory, tc.file, tc.content)
			}

			_, err := LoadSystemPrompt(directory)
			require.ErrorIs(t, err, ErrSystemPromptInvalid)
		})
	}
}

// A request may shape one turn's prompt, but not without a bound. Without this
// a caller could push an unbounded string into every provider request.
func TestRuntimeRejectsAnOversizedRequestSystemPrompt(t *testing.T) {
	fixture := newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(),
		func(o *RuntimeOptions) { o.MaxSystemPromptBytes = len("bounded") },
	)

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:          "do the thing",
		Workspace:        fixture.workspace,
		SystemPrompt:     "far longer than the configured bound",
		SystemPromptMode: PromptModeAppend,
	})
	require.ErrorIs(t, err, ErrSystemPromptTooLarge)
}

// The workspace decides where every relative tool path lands, so the model has
// to be told which directory it is working in.
func TestRuntimeSystemPromptCarriesTheWorkspace(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	prompt, err := fixture.runtime.systemPrompt(
		snapshot,
		TurnRequest{},
		fixture.workspace,
	)
	require.NoError(t, err)

	encoded, err := json.Marshal(fixture.workspace)
	require.NoError(t, err)

	assert.Contains(t, prompt, workspaceMetadataLead)
	assert.Contains(
		t,
		prompt,
		string(encoded),
		"the path must be JSON encoded, not pasted raw",
	)
}

// A deployment prompt file must actually reach the assembled prompt, not just
// parse. The wiring is what broke before: the loader did not exist at all.
func TestRuntimeUsesTheDeploymentSystemPromptFile(t *testing.T) {
	fixture := newRuntimeFixtureWithOptions(
		t,
		elelemtest.NewScriptedDriver(),
		nil,
	)
	writePromptFile(
		t,
		fixture.configDirectory,
		systemPromptFileName,
		systemPromptTestReplacement,
	)

	reloaded, err := NewRuntime(RuntimeOptions{
		Store:            fixture.store,
		Resolver:         fixture.runtime.resolver,
		Models:           fixture.runtime.models,
		RootAgent:        runtimeTestAgentName,
		DefaultModel:     runtimeTestModelReference,
		DefaultWorkspace: fixture.workspace,
		MaxContextTokens: runtimeTestMaxContextTokens,
		TurnTimeout:      runtimeTestTurnTimeout,
		ConfigDirectory:  fixture.configDirectory,
	})
	require.NoError(t, err)

	snapshot, err := reloaded.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	prompt, err := reloaded.systemPrompt(
		snapshot,
		TurnRequest{},
		fixture.workspace,
	)
	require.NoError(t, err)

	assert.Contains(t, prompt, systemPromptTestReplacement)
	assert.NotContains(t, prompt, defaultSystemPrompt)
}

func writePromptFile(
	t *testing.T,
	directory string,
	name string,
	content string,
) {
	t.Helper()

	require.NoError(t, os.WriteFile(
		filepath.Join(directory, name),
		[]byte(content),
		systemPromptTestFileMode,
	))
}
