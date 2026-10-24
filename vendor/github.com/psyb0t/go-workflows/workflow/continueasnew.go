package workflow

import (
	"github.com/psyb0t/ctxerrors"

	"github.com/psyb0t/go-workflows/backend/metadata"
	a "github.com/psyb0t/go-workflows/internal/args"
	"github.com/psyb0t/go-workflows/internal/contextvalue"
	"github.com/psyb0t/go-workflows/internal/continueasnew"
)

// ContinueAsNew restarts the current workflow with the given arguments.
func ContinueAsNew(ctx Context, args ...any) error {
	// Capture context
	propagators := propagators(ctx)
	metadata := &metadata.WorkflowMetadata{}
	if err := injectFromWorkflow(ctx, metadata, propagators); err != nil {
		return ctxerrors.Wrap(err, "injecting workflow context")
	}

	cv := contextvalue.Converter(ctx)
	inputs, err := a.ArgsToInputs(cv, args...)
	if err != nil {
		return ctxerrors.Wrap(err, "converting inputs for continuing workflow execution")
	}

	return continueasnew.NewError(metadata, inputs)
}
