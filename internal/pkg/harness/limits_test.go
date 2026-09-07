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
