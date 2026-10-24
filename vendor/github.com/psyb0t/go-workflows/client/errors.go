package client

import "errors"

// ErrWorkflowCanceled is returned when a workflow was already canceled.
var ErrWorkflowCanceled = errors.New("workflow canceled")

// ErrWorkflowTerminated is returned when a workflow was already terminated.
var ErrWorkflowTerminated = errors.New("workflow terminated")
