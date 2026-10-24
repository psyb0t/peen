package workflow

import (
	"context"
	"log/slog"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"

	"github.com/psyb0t/go-workflows/internal/contextvalue"
	"github.com/psyb0t/go-workflows/internal/log"

	"github.com/psyb0t/go-workflows/internal/workflowstate"
)

// Logger returns the logger for the current workflow.
func Logger(ctx Context) *slog.Logger {
	wfState := workflowstate.WorkflowState(ctx)
	logContext, err := ctxscope.FromJSON(context.Background(), contextvalue.LogScope(ctx))
	if err != nil {
		panic(ctxerrors.Wrap(err, "restore workflow logger scope"))
	}
	logContext = ctxscope.Set(logContext,
		ctxscope.Attr("workflow_id", wfState.Instance().InstanceID),
		ctxscope.Attr("workflow_run_id", wfState.Instance().ExecutionID),
	)
	return log.WithContext(wfState.Logger(), logContext)
}
