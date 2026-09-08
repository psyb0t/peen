package db

import (
	"testing"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGormErrorsMapToCommonErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		foreign error
		common  error
	}{
		{
			name:    "record not found",
			foreign: gorm.ErrRecordNotFound,
			common:  commerr.ErrNotFound,
		},
		{
			name:    "duplicated key",
			foreign: gorm.ErrDuplicatedKey,
			common:  commerr.ErrAlreadyExists,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ctxerrors.Wrap(tc.foreign, "database operation")

			require.ErrorIs(t, err, tc.common)
			require.NotErrorIs(t, err, tc.foreign)
		})
	}
}
