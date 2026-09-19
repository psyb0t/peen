package docker

import (
	"net/http"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/require"
)

func TestAttachStreamRequiresProtocolSwitch(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		status  int
		wantErr error
	}{
		{
			name:   "switches protocols",
			status: http.StatusSwitchingProtocols,
		},
		{
			name:    "ordinary success cannot preserve the hijacked stream",
			status:  http.StatusOK,
			wantErr: commerr.ErrExecFailed,
		},
		{
			name:    "daemon failure",
			status:  http.StatusInternalServerError,
			wantErr: commerr.ErrExecFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := assertAttachStreamAccepted(tc.status)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}
