package main

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRandom(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // Seeded on purpose.
}

func writeScenario(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "scenario.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestLoadScenarioDefaultsTheMainSession(t *testing.T) {
	t.Parallel()

	parsed, err := loadScenario(writeScenario(t, `{
		"steps": [{"op": "send", "message": "hello"}]
	}`))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"main": "/app"}, parsed.Sessions)
	assert.Equal(t, "main", parsed.Steps[0].Session)
}

func TestLoadScenarioRefusesBadInput(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{
			name: "unknown field",
			body: `{"steps":[{"op":"send","message":"x"}],"x":1}`,
		},
		{
			name:    "no steps",
			body:    `{"steps":[]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "unknown op",
			body:    `{"steps":[{"op":"dance"}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "empty message",
			body:    `{"steps":[{"op":"send","message":"  "}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "unknown session",
			body:    `{"steps":[{"op":"send","message":"x","session":"s2"}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "bad kill target",
			body:    `{"steps":[{"op":"kill","target":"grader"}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "kill step with the stop target",
			body:    `{"steps":[{"op":"kill","target":"stop"}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "send with a bad kill target",
			body: `{"steps":[{"op":"send","message":"x",` +
				`"kill":{"target":"grader","afterToolUses":1}}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "exec without command",
			body:    `{"steps":[{"op":"exec"}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name:    "event without type",
			body:    `{"steps":[{"op":"postEvent"}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "bad nested step",
			body: `{"steps":[{"op":"repeat","count":2,` +
				`"steps":[{"op":"x"}]}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "nested unknown session",
			body: `{"sessions":{"a":"/work/a"},"steps":[{"op":"parallel",` +
				`"steps":[{"op":"send","session":"c","message":"y"}]}]}`,
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadScenario(writeScenario(t, tc.body))
			require.Error(t, err)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

func TestLoadScenarioAcceptsAStopAtAToolCall(t *testing.T) {
	t.Parallel()

	parsed, err := loadScenario(writeScenario(t, `{"steps":[{"op":"send",
		"message":"x","kill":{"target":"stop","afterToolUses":3}}]}`))
	require.NoError(t, err)
	assert.Equal(t, killTargetStop, parsed.Steps[0].Kill.Target)
}

func TestExpandIterationReplacesTheToken(t *testing.T) {
	t.Parallel()

	steps := []step{{
		Op:      opSend,
		Label:   "turn-{i}",
		Message: "write file {i}",
		Steps:   []step{{Op: opPostEvent, Summary: "event {i}"}},
	}}

	expanded := expandIteration(steps, 7)

	assert.Equal(t, "turn-7", expanded[0].Label)
	assert.Equal(t, "write file 7", expanded[0].Message)
	assert.Equal(t, "event 7", expanded[0].Steps[0].Summary)
	assert.Equal(t, "write file {i}", steps[0].Message, "original unchanged")
}

func TestKillPointStaysInsideItsRange(t *testing.T) {
	t.Parallel()

	r := &runner{random: newTestRandom(1)}

	assert.Zero(t, r.killPoint(nil))
	assert.Equal(t, 4, r.killPoint(&kill{AfterToolUses: 4}))

	seen := map[int]bool{}

	for range 200 {
		point := r.killPoint(&kill{AfterToolUses: 2, AfterToolUsesMax: 5})
		require.GreaterOrEqual(t, point, 2)
		require.LessOrEqual(t, point, 5)

		seen[point] = true
	}

	assert.Len(t, seen, 4, "every point in the range is reachable")
}

func TestKillPointIgnoresToolCallsForAFileTrigger(t *testing.T) {
	t.Parallel()

	r := &runner{random: newTestRandom(1)}

	assert.Zero(t, r.killPoint(&kill{AfterToolUses: 3, OnFile: "/tmp/x"}))
}

func TestRemoveKillFileClearsAStaleFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "compacting")

	require.NoError(t, removeKillFile(path), "a missing file is fine")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, removeKillFile(path))
	assert.NoFileExists(t, path)
}

func TestWatchKillFileStopsWhenTheTurnEnds(t *testing.T) {
	t.Parallel()

	state := &sendState{
		current: step{Kill: &kill{OnFile: filepath.Join(t.TempDir(), "x")}},
	}
	watchCtx, stop := context.WithCancel(t.Context())

	state.killDone.Add(1)

	go state.watchKillFile(t.Context(), watchCtx)

	stop()
	state.killDone.Wait()

	assert.False(t, state.isKilled)
}

func TestKillPointIsReproducibleForOneSeed(t *testing.T) {
	t.Parallel()

	spec := &kill{AfterToolUses: 1, AfterToolUsesMax: 50}
	first := &runner{random: newTestRandom(9)}
	second := &runner{random: newTestRandom(9)}

	for range 20 {
		assert.Equal(t, first.killPoint(spec), second.killPoint(spec))
	}
}
