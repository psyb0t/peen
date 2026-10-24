package workflow

import (
	"time"

	"github.com/psyb0t/go-workflows/internal/workflowstate"
)

// Now returns the current time.
func Now(ctx Context) time.Time {
	wfState := workflowstate.WorkflowState(ctx)
	return wfState.Time()
}
