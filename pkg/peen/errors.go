package peen

import (
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// ErrClosed reports a call admitted after runtime shutdown began.
var ErrClosed = commerr.ErrClosed

// ErrTurnTimeout reports a turn that ran past MessageRequest.Timeout. Peen
// cancels the turn, and SendMessage returns an error that matches it with
// errors.Is.
var ErrTurnTimeout = session.ErrTurnTimeout
