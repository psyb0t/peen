package oneshot

import "errors"

// ErrQuestionUnanswerable reports that the agent asked a question while no
// terminal was attached to answer it, so the run cancelled the turn.
var ErrQuestionUnanswerable = errors.New(
	"the agent asked a question and no terminal is attached to answer it",
)
