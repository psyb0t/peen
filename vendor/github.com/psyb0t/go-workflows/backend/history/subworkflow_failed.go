package history

import "github.com/psyb0t/go-workflows/internal/workflowerrors"

type SubWorkflowFailedAttributes struct {
	Error *workflowerrors.Error `json:"error,omitempty"`
}
