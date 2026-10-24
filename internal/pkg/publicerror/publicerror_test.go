package publicerror

import (
	"errors"
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/stretchr/testify/assert"
)

var errBase = errors.New("base failure")

func TestMessage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "plain error", err: errBase, want: "base failure"},
		{
			name: "wrapped once",
			err:  ctxerrors.Wrap(errBase, "load skill"),
			want: "load skill: base failure",
		},
		{
			name: "wrapped twice",
			err: ctxerrors.Wrap(
				ctxerrors.Wrap(errBase, "load skill"),
				"resolve harness",
			),
			want: "resolve harness: load skill: base failure",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			message := Message(tc.err)

			assert.Equal(t, tc.want, message)
			assert.NotContains(t, message, locationMarker)
			assert.NotContains(t, message, ".go:")
		})
	}
}
