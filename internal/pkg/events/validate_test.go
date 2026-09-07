package events

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateType(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		eventType Type
		wantErr   error
	}{
		{name: "single segment", eventType: "external"},
		{name: "two segments", eventType: "app.error"},
		{name: "digits after a letter", eventType: "ci2.build3"},
		{name: "peen job type", eventType: TypeJobExited},
		{name: "peen agent type", eventType: TypeAgentFinished},
		{name: "empty", wantErr: ErrInvalidType},
		{
			name:      "uppercase",
			eventType: "App.Error",
			wantErr:   ErrInvalidType,
		},
		{
			name:      "leading digit in a segment",
			eventType: "app.1error",
			wantErr:   ErrInvalidType,
		},
		{
			name:      "empty segment",
			eventType: "app..error",
			wantErr:   ErrInvalidType,
		},
		{
			name:      "trailing separator",
			eventType: "app.",
			wantErr:   ErrInvalidType,
		},
		{
			name:      "underscore",
			eventType: "app.my_error",
			wantErr:   ErrInvalidType,
		},
		{
			name:      "hyphen",
			eventType: "app.my-error",
			wantErr:   ErrInvalidType,
		},
		{
			name:      "too long",
			eventType: strings.Repeat("a", maxTypeLength+1),
			wantErr:   ErrInvalidType,
		},
		{
			name:      "too many segments",
			eventType: "a.b.c.d.e.f.g.h.i",
			wantErr:   ErrInvalidType,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateType(tc.eventType)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// An outside caller must not be able to forge a job or agent event, because
// those carry Peen's own guarantees about what actually happened.
func TestValidateExternalTypeRejectsReservedPrefixes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		eventType Type
		wantErr   error
	}{
		{name: "deployment type", eventType: "app.error"},
		{name: "another deployment type", eventType: "ci.failed"},
		{
			name:      "job namespace",
			eventType: TypeJobExited,
			wantErr:   ErrReservedType,
		},
		{
			name:      "agent namespace",
			eventType: TypeAgentFinished,
			wantErr:   ErrReservedType,
		},
		{
			name:      "invented job type",
			eventType: "job.whatever",
			wantErr:   ErrReservedType,
		},
		{
			name:      "malformed beats reserved",
			eventType: "job.Bad",
			wantErr:   ErrInvalidType,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateExternalType(tc.eventType)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// A type that merely starts with the same letters as a reserved namespace is
// not reserved. Only the dotted prefix is.
func TestIsReservedMatchesNamespaceNotPrefixText(t *testing.T) {
	t.Parallel()

	assert.True(t, IsReserved("job.exited"))
	assert.True(t, IsReserved("agent.finished"))
	assert.False(t, IsReserved("jobs.exited"))
	assert.False(t, IsReserved("agentic.thing"))
	assert.False(t, IsReserved("app.error"))
}

func TestValidateDelivery(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		delivery Delivery
		want     Delivery
		wantErr  error
	}{
		{name: "empty means queue", delivery: "", want: DeliveryQueue},
		{name: "queue", delivery: DeliveryQueue, want: DeliveryQueue},
		{name: "wake", delivery: DeliveryWake, want: DeliveryWake},
		{
			name:     "unknown",
			delivery: "explode",
			wantErr:  ErrInvalidDelivery,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ValidateDelivery(tc.delivery)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
