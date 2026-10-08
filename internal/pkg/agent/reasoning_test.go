package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFitReasoningEffort(t *testing.T) {
	t.Parallel()

	effortModel := elelem.Capabilities{
		SupportsReasoningEffort: true,
		MaxReasoningEffort:      elelem.ReasoningEffortHigh,
	}
	openRange := elelem.Capabilities{SupportsReasoningEffort: true}
	lowFloor := elelem.Model{
		ReasoningLevels: elelem.ReasoningLevels{Min: elelem.ReasoningEffortLow},
	}

	testCases := []struct {
		name         string
		requested    elelem.ReasoningEffort
		model        elelem.Model
		capabilities elelem.Capabilities
		wantEffort   elelem.ReasoningEffort
		wantReason   string
	}{
		{
			name:         "nothing requested",
			capabilities: effortModel,
		},
		{
			name:         "model without effort support",
			requested:    elelem.ReasoningEffortMedium,
			capabilities: elelem.Capabilities{},
			wantReason:   reasoningEffortReasonUnsupported,
		},
		{
			name:         "level inside the range",
			requested:    elelem.ReasoningEffortMedium,
			capabilities: effortModel,
			wantEffort:   elelem.ReasoningEffortMedium,
		},
		{
			name:         "level at the maximum",
			requested:    elelem.ReasoningEffortHigh,
			capabilities: effortModel,
			wantEffort:   elelem.ReasoningEffortHigh,
		},
		{
			name:         "level above the maximum",
			requested:    elelem.ReasoningEffortMax,
			capabilities: effortModel,
			wantEffort:   elelem.ReasoningEffortHigh,
			wantReason:   reasoningEffortReasonAboveMaximum,
		},
		{
			name:         "no stated maximum",
			requested:    elelem.ReasoningEffortMax,
			capabilities: openRange,
			wantEffort:   elelem.ReasoningEffortMax,
		},
		{
			name:         "level below the minimum",
			requested:    elelem.ReasoningEffortMinimal,
			model:        lowFloor,
			capabilities: openRange,
			wantEffort:   elelem.ReasoningEffortLow,
			wantReason:   reasoningEffortReasonBelowMinimum,
		},
		{
			name:         "level at the minimum",
			requested:    elelem.ReasoningEffortLow,
			model:        lowFloor,
			capabilities: openRange,
			wantEffort:   elelem.ReasoningEffortLow,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			effort, reason := fitReasoningEffort(
				tc.requested,
				tc.model,
				tc.capabilities,
			)
			assert.Equal(t, tc.wantEffort, effort)
			assert.Equal(t, tc.wantReason, reason)
		})
	}
}

func TestRuntimeSendsTheRequestedReasoningEffort(t *testing.T) {
	testCases := []struct {
		name      string
		requested *string
		want      elelem.ReasoningEffort
	}{
		{name: "level requested", requested: new("xhigh"), want: "xhigh"},
		{name: "no level requested", want: elelem.ReasoningEffortUnset},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
			fixture := newRuntimeFixture(t, driver)

			_, err := fixture.runtime.RunMessage(
				context.Background(),
				MessageRequest{
					Message:         "inspect",
					ReasoningEffort: tc.requested,
				},
				uuid.New(),
				nil,
			)
			require.NoError(t, err)

			requests := driver.Requests()
			require.Len(t, requests, 1)
			assert.Equal(t, tc.want, requests[0].Params.ReasoningEffort)
		})
	}
}

func TestRuntimeRejectsTurnSettingsInAnActiveTurnQueue(t *testing.T) {
	testCases := []struct {
		name    string
		request TurnRequest
	}{
		{
			name:    "model",
			request: TurnRequest{Message: "queued", Model: "other/model"},
		},
		{
			name: "reasoning effort",
			request: TurnRequest{
				Message:         "queued",
				ReasoningEffort: elelem.ReasoningEffortHigh,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := elelemtest.NewScriptedDriver(
				elelemtest.ToolCall(
					runtimeToolCallID,
					toolNameListFiles,
					`{"path":"."}`,
				),
				elelemtest.Text(queuedUserMessageFinal),
			)
			fixture := newRuntimeFixture(t, driver)

			var queueErr error
			_, err := fixture.runtime.Run(context.Background(), TurnRequest{
				Message:   queuedUserMessageInitial,
				Workspace: fixture.workspace,
				OnEvent: func(event Event) error {
					if event.Type == EventTypeToolUse {
						_, queueErr = fixture.runtime.Run(
							context.Background(),
							tc.request,
						)
					}

					return nil
				},
			})
			require.NoError(t, err)
			require.ErrorIs(t, queueErr, commerr.ErrConflict)

			requests := driver.Requests()
			require.Len(t, requests, 2)
			assert.NotContains(
				t,
				requests[1].Messages[len(requests[1].Messages)-1].Text(),
				"queued",
			)
		})
	}
}
