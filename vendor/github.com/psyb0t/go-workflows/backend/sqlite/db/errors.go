package db

import "errors"

// errNoPendingEvents rolls back a workflow claim whose instance has no visible
// pending events left, so no empty task is handed to a worker.
var errNoPendingEvents = errors.New("workflow instance has no pending events")
