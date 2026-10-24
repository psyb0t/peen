package backend

import (
	"errors"
	"fmt"
)

var (
	ErrInstanceNotFound      = errors.New("workflow instance not found")
	ErrInstanceAlreadyExists = errors.New("workflow instance already exists")
	ErrInstanceNotFinished   = errors.New("workflow instance is not finished")
)

type ErrNotSupported struct {
	Message string
}

func (e ErrNotSupported) Error() string {
	return fmt.Sprintf("not supported: %s", e.Message)
}
