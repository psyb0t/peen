package harness

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotCheckpointRetainsResolvedContentAfterSourceChanges(t *testing.T) {
	t.Parallel()
	fixture := newResolverFixture(t)
	fixture.writeAgents(t, fixture.workspace, "original instructions")
	fixture.writeSkill(t, fixture.workspace,
		testSkillName, "original description", "original skill body")
	snapshot := resolveFixture(t, fixture)
	encoded, err := snapshot.Checkpoint()
	require.NoError(t, err)
	fixture.writeAgents(t, fixture.workspace, "changed instructions")
	fixture.writeSkill(t, fixture.workspace,
		testSkillName, "changed description", "changed skill body")
	restored, err := RestoreCheckpoint(encoded)
	require.NoError(t, err)
	assert.Equal(t, snapshot, restored)
	assert.NotEqual(t, snapshot.Hash(), resolveFixture(t, fixture).Hash())
	activated, err := restored.ActivateSkill(testSkillName)
	require.NoError(t, err)
	assert.Contains(t, activated.Content, "original skill body")
	assert.NotContains(t, activated.Content, "changed skill body")
	reencoded, err := restored.Checkpoint()
	require.NoError(t, err)
	assert.JSONEq(t, string(encoded), string(reencoded))
}

func TestSnapshotCheckpointRetainsHooksHandlersAndBudget(t *testing.T) {
	t.Parallel()
	snapshot := testSnapshot()
	snapshot.skillBudget = 1
	snapshot.hooks = []Hook{{
		Event: HookEventPreToolUse, Actions: []HookAction{{
			Type: HookActionInject, Message: "saved hook",
		}},
	}}
	snapshot.eventHandlers = []EventHandler{{
		Type: "example.created", Instructions: "saved handler",
	}}
	snapshot.warnings = []Warning{{Reason: "saved warning"}}
	encoded, err := snapshot.Checkpoint()
	require.NoError(t, err)
	restored, err := RestoreCheckpoint(encoded)
	require.NoError(t, err)
	assert.Equal(t, snapshot, restored)
	_, originalErr := snapshot.ExpandSkills([]string{testSkillName}, nil)
	_, restoredErr := restored.ExpandSkills([]string{testSkillName}, nil)
	require.ErrorIs(t, originalErr, ErrSkillContentTooLarge)
	require.ErrorIs(t, restoredErr, ErrSkillContentTooLarge)
	restored.hooks[0].Actions[0].Message = "changed"
	assert.Equal(t, "saved hook", snapshot.hooks[0].Actions[0].Message)
}

func TestRestoreCheckpointRejectsMalformedData(t *testing.T) {
	t.Parallel()
	encoded, err := testSnapshot().Checkpoint()
	require.NoError(t, err)
	var checkpoint snapshotCheckpoint
	require.NoError(t, json.Unmarshal(encoded, &checkpoint))
	corrupt := checkpoint
	corrupt.State = json.RawMessage(`{}`)
	corruptBytes, err := json.Marshal(corrupt)
	require.NoError(t, err)
	testCases := []struct {
		name  string
		input json.RawMessage
	}{
		{name: "empty"},
		{name: "null", input: json.RawMessage(`null`)},
		{name: "array", input: json.RawMessage(`[]`)},
		{name: "missing state", input: json.RawMessage(`{}`)},
		{name: "unknown field", input: json.RawMessage(`{"unknown":1}`)},
		{name: "trailing data", input: append(encoded, []byte(`{}`)...)},
		{name: "corrupt state", input: corruptBytes},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := RestoreCheckpoint(tc.input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestSnapshotCheckpointReturnsEncodingFailure(t *testing.T) {
	t.Parallel()
	snapshot := testSnapshot()
	snapshot.skills[0].Metadata["invalid"] = make(chan struct{})
	_, err := snapshot.Checkpoint()
	var unsupported *json.UnsupportedTypeError
	require.ErrorAs(t, err, &unsupported)
}

func TestSnapshotCheckpointRetainsLargeMetadataNumbers(t *testing.T) {
	t.Parallel()
	snapshot := testSnapshot()
	snapshot.skills[0].Metadata["quantity"] = uint64(math.MaxUint64)
	encoded, err := snapshot.Checkpoint()
	require.NoError(t, err)
	restored, err := RestoreCheckpoint(encoded)
	require.NoError(t, err)
	reencoded, err := restored.Checkpoint()
	require.NoError(t, err)
	assert.JSONEq(t, string(encoded), string(reencoded))
	var state snapshotCheckpoint
	require.NoError(t, json.Unmarshal(reencoded, &state))
	assert.Contains(t, string(state.State), "18446744073709551615")
}

func TestRestoreCheckpointValidatesVersionAndState(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		version int
		state   json.RawMessage
	}{
		{
			name: "unknown version", version: snapshotCheckpointVersion + 1,
			state: json.RawMessage(`{}`),
		},
		{
			name: "negative budget", version: snapshotCheckpointVersion,
			state: json.RawMessage(`{"skillBudget":-1}`),
		},
		{
			name: "null state", version: snapshotCheckpointVersion,
			state: json.RawMessage(`null`),
		},
		{
			name: "unknown state field", version: snapshotCheckpointVersion,
			state: json.RawMessage(`{"unknown":true}`),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(snapshotCheckpoint{
				Version: tc.version, State: tc.state,
				Digest: hashString(string(tc.state)),
			})
			require.NoError(t, err)
			_, err = RestoreCheckpoint(encoded)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}
