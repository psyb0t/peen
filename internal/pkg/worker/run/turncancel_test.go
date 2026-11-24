package run

import (
	"encoding/json"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/require"
)

func TestExactCancellationRejectsMissingOrMalformedTurnIDs(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{"{}", "null"} {
		_, err := newCommandHandler().cancelExactTurn(t.Context(),
			json.RawMessage(payload))
		require.ErrorIs(t, err, commerr.ErrValidationFailed)
	}
	for _, payload := range []string{"not JSON", `{"turnId":"bad"}`} {
		_, err := newCommandHandler().cancelExactTurn(t.Context(),
			json.RawMessage(payload))
		require.Error(t, err)
	}
}
