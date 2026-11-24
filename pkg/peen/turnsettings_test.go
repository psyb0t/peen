package peen

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedMessageReturnsStructuredOutput(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("draft answer"), elelemtest.Text(`{"answer":7}`))
	runtime, err := New(workflowEmbeddingOptions(t, driver))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, runtime.Close(context.Background()))
	})
	current := runtime.Session(runtime.internal.SessionID().String())
	result, err := current.Send(t.Context(), MessageRequest{
		Message: "produce an answer", Timeout: time.Minute,
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "draft answer", result.Message)
	assert.JSONEq(t, `{"answer":7}`, string(result.Output))
	assert.Empty(t, result.OutputError)
}

func TestEmbeddedMessageTimeoutMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		seconds int
		invalid bool
	}{
		{name: "absent"},
		{name: "round up", timeout: time.Second + 1, seconds: 2},
		{name: "negative", timeout: -time.Second, invalid: true},
		{name: "over maximum", timeout: 25 * time.Hour, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			request, _, err := (MessageRequest{
				Message: "answer", Timeout: tc.timeout,
				OutputSchema: json.RawMessage(`{}`),
			}).internalRequest(uuid.New())
			if tc.invalid {
				require.ErrorIs(t, err, commerr.ErrValidationFailed)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.seconds, request.TimeoutSeconds)
			assert.JSONEq(t, `{}`, string(request.OutputSchema))
		})
	}
}

func TestEmbeddedTurnTimeoutWinsOverCancellation(t *testing.T) {
	t.Parallel()
	timedOut := translateTurnError(
		errors.Join(context.Canceled, ErrTurnTimeout))
	require.ErrorIs(t, timedOut, ErrTurnTimeout)
	assert.NotErrorIs(t, timedOut, commerr.ErrCancelled)
	cancelled := translateTurnError(context.Canceled)
	require.ErrorIs(t, cancelled, commerr.ErrCancelled)
	assert.NotErrorIs(t, cancelled, ErrTurnTimeout)
}
