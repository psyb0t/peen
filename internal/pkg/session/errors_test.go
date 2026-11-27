package session

import (
	"errors"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/assert"
)

func TestIsSessionBusy(t *testing.T) {
	t.Parallel()

	conflict := &AgentSpaceConflictError{
		Current: "docker:default", Requested: "control",
	}
	tests := []struct {
		name string
		err  error
		busy bool
	}{
		{name: "store busy", err: ErrSessionBusy, busy: true},
		{
			name: "wrapped store busy",
			err:  ctxerrors.Wrap(ErrSessionBusy, "start turn"), busy: true,
		},
		{
			name: "worker busy",
			err:  ctxerrors.Wrap(commerr.ErrConflict, "active turn"),
			busy: true,
		},
		{name: "agent space conflict", err: conflict, busy: false},
		{
			name: "wrapped agent space conflict",
			err:  ctxerrors.Wrap(conflict, "open session"), busy: false,
		},
		{
			name: "joined agent space conflict",
			err:  errors.Join(commerr.ErrConflict, conflict),
			busy: false,
		},
		{name: "not found", err: commerr.ErrNotFound, busy: false},
		{name: "nil", err: nil, busy: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.busy, IsSessionBusy(test.err))
		})
	}
}
