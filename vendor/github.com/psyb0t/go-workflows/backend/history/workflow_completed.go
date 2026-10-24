package history

import (
	"github.com/psyb0t/go-workflows/backend/payload"
	"github.com/psyb0t/go-workflows/internal/workflowerrors"
)

type ExecutionCompletedAttributes struct {
	Result payload.Payload       `json:"result,omitempty"`
	Error  *workflowerrors.Error `json:"error,omitempty"`
}
