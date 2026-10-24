package continueasnew

import (
	"github.com/psyb0t/go-workflows/backend/metadata"
	"github.com/psyb0t/go-workflows/backend/payload"
)

type Error struct {
	Metadata *metadata.WorkflowMetadata
	Inputs   []payload.Payload
}

var _ error = (*Error)(nil)

func (e *Error) Error() string {
	return "ContinueAsNew"
}

func NewError(metadata *metadata.WorkflowMetadata, inputs []payload.Payload) error {
	return &Error{
		Metadata: metadata,
		Inputs:   inputs,
	}
}
