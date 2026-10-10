package agent

import (
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireKnownSkillReferences(t *testing.T) {
	t.Parallel()

	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())
	writeSkillFile(t, fixture, skillTestAlpha, skillTestBravoBody)

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	testCases := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{name: "no reference", text: "plain text"},
		{name: "custom skill", text: "use :" + skillTestAlpha},
		{name: "core skill", text: "use :core:planning"},
		{name: "plain name falls back to core", text: "use :planning"},
		{name: "unknown skill", text: "use :ghost-skill", wantErr: true},
		{name: "unknown core skill", text: "use :core:ghost", wantErr: true},
		{name: "not a reference", text: "path:ghost-skill"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := requireKnownSkillReferences(snapshot, tc.text)
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, harness.ErrSkillNotFound)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			assert.Contains(t, err.Error(), skillTestAlpha)

			var unknown *harness.UnknownSkillError
			require.ErrorAs(t, err, &unknown)
			assert.Contains(t, unknown.Message, "unknown skill ")
			assert.Contains(t, unknown.Message, "; available skills: ")
			assert.Contains(t, unknown.Message, skillTestAlpha)
			assert.Contains(t, unknown.Message, "core:planning")
			assert.NotContains(t, unknown.Message, ".go:")
		})
	}
}

func TestUnknownSkillReferenceMessageNamesTheWrongName(t *testing.T) {
	t.Parallel()

	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())
	writeSkillFile(t, fixture, skillTestAlpha, skillTestBravoBody)

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	err = requireKnownSkillReferences(snapshot, "use :ghost-skill now")

	var unknown *harness.UnknownSkillError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(
		t,
		`unknown skill "ghost-skill" in :ghost-skill; available skills: `+
			`core:freshness, core:planning, `+skillTestAlpha,
		unknown.Message,
	)
}
