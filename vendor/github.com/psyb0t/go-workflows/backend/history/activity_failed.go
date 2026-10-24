package history

import "github.com/psyb0t/go-workflows/internal/workflowerrors"

type ActivityFailedAttributes struct {
	Error *workflowerrors.Error `json:"error,omitempty"`
}
