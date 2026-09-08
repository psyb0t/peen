package db

import (
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"gorm.io/gorm"
)

//nolint:gochecknoinits // Register mappings before database calls.
func init() {
	ctxerrors.MapError(gorm.ErrRecordNotFound, commerr.ErrNotFound)
	ctxerrors.MapError(gorm.ErrDuplicatedKey, commerr.ErrAlreadyExists)
}
