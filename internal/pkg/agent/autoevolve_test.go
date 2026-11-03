package agent

import (
	"path/filepath"
	"testing"

	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	autoEvolveTestHeading     = "## Learning"
	autoEvolveTestWritingLead = "The user made the main configuration directory"
	autoEvolveTestChildTask   = "child instructions"
)

// With auto-evolve on, the root and child prompts name the workspace's
// learned directory and the global one, and only a writable config directory
// adds the sentence that lets the agent change it.
func TestRuntimeSystemPromptsCarryTheLearningSection(t *testing.T) {
	testCases := []struct {
		name           string
		autoEvolve     bool
		writableConfig bool
	}{
		{name: "auto-evolve off"},
		{name: "auto-evolve on", autoEvolve: true},
		{
			name:           "auto-evolve with a writable config directory",
			autoEvolve:     true,
			writableConfig: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			globalLearned := filepath.Join(t.TempDir(), "learned")

			fixture := newRuntimeFixtureWithOptions(
				t,
				elelemtest.NewScriptedDriver(),
				func(o *RuntimeOptions) {
					o.AutoEvolve = tc.autoEvolve
					o.LearnedDirectory = globalLearned
					o.WritableConfigDirectory = tc.writableConfig
				},
			)

			snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
			require.NoError(t, err)

			rootPrompt, err := fixture.runtime.systemPrompt(
				snapshot,
				TurnRequest{},
				fixture.workspace,
			)
			require.NoError(t, err)

			childPrompt, err := fixture.runtime.childSystemPrompt(
				snapshot,
				autoEvolveTestChildTask,
				fixture.workspace,
			)
			require.NoError(t, err)

			workspaceLearned := filepath.Join(
				fixture.workspace,
				agentsDirectoryName,
				learnedDirectoryName,
			)

			for _, prompt := range []string{rootPrompt, childPrompt} {
				if !tc.autoEvolve {
					assert.NotContains(t, prompt, autoEvolveTestHeading)

					continue
				}

				assert.Contains(t, prompt, autoEvolveTestHeading)
				assert.Contains(t, prompt, workspaceLearned)
				assert.Contains(t, prompt, globalLearned)
				assert.Contains(t, prompt, "core:writing-skills")
				assert.Contains(t, prompt, "core:writing-rules")

				if tc.writableConfig {
					assert.Contains(t, prompt, autoEvolveTestWritingLead)
					assert.Contains(t, prompt, fixture.configDirectory)

					continue
				}

				assert.NotContains(t, prompt, autoEvolveTestWritingLead)
			}
		})
	}
}

// A global learned directory that is the workspace's own is named once, and a
// workspace that is not a host path gets a relative learned directory.
func TestAutoEvolveBlockPaths(t *testing.T) {
	t.Parallel()

	workspace := "/srv/work/project"
	ownLearned := filepath.Join(
		workspace,
		agentsDirectoryName,
		learnedDirectoryName,
	)

	runtime := &Runtime{autoEvolve: true, learnedDirectory: ownLearned}

	block, err := runtime.autoEvolveBlock(workspace)
	require.NoError(t, err)
	assert.Contains(t, block, "- "+ownLearned+" for what belongs")
	assert.NotContains(t, block, "for what applies to every project")

	virtual, err := runtime.autoEvolveBlock("memory-workspace")
	require.NoError(t, err)
	assert.Contains(
		t,
		virtual,
		"- "+filepath.Join(agentsDirectoryName, learnedDirectoryName)+
			" for what belongs",
	)
}
