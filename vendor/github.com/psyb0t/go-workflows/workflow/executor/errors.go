package executor

import "errors"

// ErrExecutorClosed is returned by ExecuteTask when the executor was closed
// before the task started. Callers create a new executor for the task.
var ErrExecutorClosed = errors.New("workflow executor closed")
