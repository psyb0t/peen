package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimitsWithDefaults(t *testing.T) {
	t.Parallel()

	defaults := defaultLimits()
	customMaxFiles := defaults.MaxFiles - 1
	customMaxFileBytes := defaults.MaxFileBytes - 1

	testCases := []struct {
		name  string
		input Limits
		want  Limits
	}{
		{
			name:  "all zero fields use defaults",
			input: Limits{},
			want:  defaults,
		},
		{
			name: "configured values are preserved",
			input: Limits{
				MaxFiles:             customMaxFiles,
				MaxInstructions:      defaults.MaxInstructions,
				MaxSkills:            defaults.MaxSkills,
				MaxAgents:            defaults.MaxAgents,
				MaxEventHandlers:     defaults.MaxEventHandlers,
				MaxHooks:             defaults.MaxHooks,
				MaxDirectoryEntries:  defaults.MaxDirectoryEntries,
				MaxFileBytes:         customMaxFileBytes,
				MaxTotalContextBytes: defaults.MaxTotalContextBytes,
			},
			want: Limits{
				MaxFiles:             customMaxFiles,
				MaxInstructions:      defaults.MaxInstructions,
				MaxSkills:            defaults.MaxSkills,
				MaxAgents:            defaults.MaxAgents,
				MaxEventHandlers:     defaults.MaxEventHandlers,
				MaxHooks:             defaults.MaxHooks,
				MaxDirectoryEntries:  defaults.MaxDirectoryEntries,
				MaxFileBytes:         customMaxFileBytes,
				MaxTotalContextBytes: defaults.MaxTotalContextBytes,
			},
		},
		{
			name: "zero field receives only its own default",
			input: Limits{
				MaxFiles:             customMaxFiles,
				MaxInstructions:      defaults.MaxInstructions,
				MaxSkills:            defaults.MaxSkills,
				MaxAgents:            defaults.MaxAgents,
				MaxEventHandlers:     defaults.MaxEventHandlers,
				MaxHooks:             defaults.MaxHooks,
				MaxDirectoryEntries:  defaults.MaxDirectoryEntries,
				MaxTotalContextBytes: defaults.MaxTotalContextBytes,
			},
			want: Limits{
				MaxFiles:             customMaxFiles,
				MaxInstructions:      defaults.MaxInstructions,
				MaxSkills:            defaults.MaxSkills,
				MaxAgents:            defaults.MaxAgents,
				MaxEventHandlers:     defaults.MaxEventHandlers,
				MaxHooks:             defaults.MaxHooks,
				MaxDirectoryEntries:  defaults.MaxDirectoryEntries,
				MaxFileBytes:         defaults.MaxFileBytes,
				MaxTotalContextBytes: defaults.MaxTotalContextBytes,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.input.withDefaults())
		})
	}
}

func TestLimitsValidate(t *testing.T) {
	t.Parallel()

	defaults := defaultLimits()
	testCases := []struct {
		name    string
		mutate  func(Limits) Limits
		wantErr error
	}{
		{name: "valid defaults"},
		{
			name: "zero file limit",
			mutate: func(limits Limits) Limits {
				limits.MaxFiles = 0

				return limits
			},
			wantErr: ErrInvalidOptions,
		},
		{
			name: "negative instruction limit",
			mutate: func(limits Limits) Limits {
				limits.MaxInstructions = -1

				return limits
			},
			wantErr: ErrInvalidOptions,
		},
		{
			name: "zero skills limit",
			mutate: func(limits Limits) Limits {
				limits.MaxSkills = 0

				return limits
			},
			wantErr: ErrInvalidOptions,
		},
		{
			name: "zero agents limit",
			mutate: func(limits Limits) Limits {
				limits.MaxAgents = 0

				return limits
			},
			wantErr: ErrInvalidOptions,
		},
		{
			name: "zero directory entry limit",
			mutate: func(limits Limits) Limits {
				limits.MaxDirectoryEntries = 0

				return limits
			},
			wantErr: ErrInvalidOptions,
		},
		{
			name: "zero file bytes",
			mutate: func(limits Limits) Limits {
				limits.MaxFileBytes = 0

				return limits
			},
			wantErr: ErrInvalidOptions,
		},
		{
			name: "total bytes below file bytes",
			mutate: func(limits Limits) Limits {
				limits.MaxTotalContextBytes = limits.MaxFileBytes - 1

				return limits
			},
			wantErr: ErrInvalidOptions,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			limits := defaults
			if tc.mutate != nil {
				limits = tc.mutate(limits)
			}

			err := limits.validate()
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestResolverCustomAgentsDoNotCountAgainstCoreAgentBound(t *testing.T) {
	t.Parallel()

	fixture := newResolverFixture(t)
	fixture.writeAgent(
		t,
		fixture.configRoot,
		embeddedGeneralPurposeAgentName,
		"configured",
		testAgentBody,
	)

	limits := defaultLimits()
	limits.MaxAgents = 1
	resolver, err := NewResolver(fixture.configRoot, limits)
	require.NoError(t, err)
	snapshot, err := resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	assert.Len(t, snapshot.Agents(), 2)
}

func TestResolverEnforcesFileAndEffectiveDefinitionBounds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		limits  func(Limits) Limits
		setup   func(t *testing.T, fixture resolverFixture)
		wantErr error
	}{
		{
			name: "file count accepts equality",
			limits: func(limits Limits) Limits {
				limits.MaxFiles = 2

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgents(t, fixture.configRoot, "config")
				fixture.writeAgents(t, fixture.workspace, "workspace")
			},
		},
		{
			name: "file count rejects one more",
			limits: func(limits Limits) Limits {
				limits.MaxFiles = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgents(t, fixture.configRoot, "config")
				fixture.writeAgents(t, fixture.workspace, "workspace")
			},
			wantErr: ErrResourceLimit,
		},
		{
			name: "instruction count rejects one more",
			limits: func(limits Limits) Limits {
				limits.MaxInstructions = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgents(t, fixture.configRoot, "config")
				fixture.writeAgents(t, fixture.workspace, "workspace")
			},
			wantErr: ErrResourceLimit,
		},
		{
			name: "skill count accepts replacement",
			limits: func(limits Limits) Limits {
				limits.MaxSkills = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeSkill(
					t,
					fixture.configRoot,
					"same-skill",
					"config",
					testSkillBody,
				)
				fixture.writeSkill(
					t,
					fixture.workspace,
					"same-skill",
					"workspace",
					testSkillBody,
				)
			},
		},
		{
			name: "skill count rejects a second effective name",
			limits: func(limits Limits) Limits {
				limits.MaxSkills = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeSkill(
					t,
					fixture.configRoot,
					"first-skill",
					"first",
					testSkillBody,
				)
				fixture.writeSkill(
					t,
					fixture.workspace,
					"second-skill",
					"second",
					testSkillBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
		{
			name: "agent count accepts replacement",
			limits: func(limits Limits) Limits {
				limits.MaxAgents = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"same-agent",
					"config",
					testAgentBody,
				)
				fixture.writeAgent(
					t,
					fixture.workspace,
					"same-agent",
					"workspace",
					testAgentBody,
				)
			},
		},
		{
			name: "agent count rejects a second effective name",
			limits: func(limits Limits) Limits {
				limits.MaxAgents = 1

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"first-agent",
					"first",
					testAgentBody,
				)
				fixture.writeAgent(
					t,
					fixture.workspace,
					"second-agent",
					"second",
					testAgentBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			tc.setup(t, fixture)
			resolver, err := NewResolver(
				fixture.configRoot,
				tc.limits(defaultLimits()),
			)
			require.NoError(t, err)

			_, err = resolver.Resolve(fixture.workspace)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestResolverEnforcesByteBoundsAtEquality(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		limits  func(Limits, resolverFixture) Limits
		setup   func(t *testing.T, fixture resolverFixture)
		wantErr error
	}{
		{
			name: "individual file accepts equality",
			limits: func(limits Limits, _ resolverFixture) Limits {
				content := agentDocument(
					"single-agent",
					"single",
					testAgentBody,
				)
				limits.MaxFileBytes = int64(len(content))

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"single-agent",
					"single",
					testAgentBody,
				)
			},
		},
		{
			name: "individual file rejects one byte more",
			limits: func(limits Limits, _ resolverFixture) Limits {
				content := agentDocument(
					"single-agent",
					"single",
					testAgentBody,
				)
				limits.MaxFileBytes = int64(len(content) - 1)

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"single-agent",
					"single",
					testAgentBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
		{
			name: "total context accepts equality",
			limits: func(limits Limits, _ resolverFixture) Limits {
				configContent := agentDocument(
					"config-agent",
					"config",
					testAgentBody,
				)
				workspaceContent := agentDocument(
					"workspace-agent",
					"workspace",
					testAgentBody,
				)
				limits.MaxFileBytes = int64(
					max(len(configContent), len(workspaceContent)),
				)
				limits.MaxTotalContextBytes = int64(
					len(configContent) + len(workspaceContent),
				)

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"config-agent",
					"config",
					testAgentBody,
				)
				fixture.writeAgent(
					t,
					fixture.workspace,
					"workspace-agent",
					"workspace",
					testAgentBody,
				)
			},
		},
		{
			name: "total context rejects one byte more",
			limits: func(limits Limits, _ resolverFixture) Limits {
				configContent := agentDocument(
					"config-agent",
					"config",
					testAgentBody,
				)
				workspaceContent := agentDocument(
					"workspace-agent",
					"workspace",
					testAgentBody,
				)
				limits.MaxFileBytes = int64(
					max(len(configContent), len(workspaceContent)),
				)
				limits.MaxTotalContextBytes = int64(
					len(configContent) + len(workspaceContent) - 1,
				)

				return limits
			},
			setup: func(t *testing.T, fixture resolverFixture) {
				t.Helper()

				fixture.writeAgent(
					t,
					fixture.configRoot,
					"config-agent",
					"config",
					testAgentBody,
				)
				fixture.writeAgent(
					t,
					fixture.workspace,
					"workspace-agent",
					"workspace",
					testAgentBody,
				)
			},
			wantErr: ErrResourceLimit,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newResolverFixture(t)
			limits := tc.limits(defaultLimits(), fixture)
			tc.setup(t, fixture)

			resolver, err := NewResolver(fixture.configRoot, limits)
			require.NoError(t, err)

			_, err = resolver.Resolve(fixture.workspace)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
