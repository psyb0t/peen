package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetainedWorkflowSubmissionValidatesSavedArguments(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name           string
		context        string
		changeIdentity bool
		wantErr        error
	}{
		{name: "original limits", context: `{"limits":{"maxRounds":7,"maxConcurrentTools":3}}`},
		{name: "changed identity", context: `{}`, changeIdentity: true, wantErr: commerr.ErrConflict},
		{name: "malformed context", context: `{`, wantErr: nil},
		{name: "missing limits", context: `{}`, wantErr: commerr.ErrValidationFailed},
		{name: "zero rounds", context: `{"limits":{"maxRounds":0,"maxConcurrentTools":3}}`, wantErr: commerr.ErrValidationFailed},
		{name: "negative concurrency", context: `{"limits":{"maxRounds":7,"maxConcurrentTools":-1}}`, wantErr: commerr.ErrValidationFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			saved := &models.Execution{ID: uuid.New(), SessionID: uuid.New(), ContextJSON: tc.context}
			saved.WorkflowExecutionID = nativeWorkflowInstance(saved.SessionID, saved.ID).ExecutionID
			if tc.changeIdentity {
				saved.WorkflowExecutionID = uuid.NewString()
			}
			input, err := retainedWorkflowSubmission(saved)
			if !json.Valid([]byte(tc.context)) {
				var syntax *json.SyntaxError
				require.ErrorAs(t, err, &syntax)

				return
			}
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantErr != nil {
				return
			}
			assert.Equal(t, saved.SessionID, input.SessionID)
			assert.Equal(t, saved.ID, input.ExecutionID)
			assert.Equal(t, 7, input.MaxRounds)
			assert.Equal(t, 3, input.MaxConcurrentTools)
			assert.JSONEq(t, saved.ContextJSON, string(input.Context))
		})
	}
}
