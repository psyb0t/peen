package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/publicerror"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	rootRoundLimitFormat = "The turn stopped at its limit of %d tool rounds (PEEN_MAX_TOOL_ROUNDS)." //nolint:lll // One user-facing sentence.

	childRoundLimitFormat = "The agent stopped at its limit of %d tool rounds (PEEN_MAX_CHILD_AGENT_TURNS)." //nolint:lll // One user-facing sentence.

	rootTokenLimitFormat = "The turn stopped after using %d of its %d token limit (PEEN_MAX_TURN_TOKENS)." //nolint:lll // One user-facing sentence.

	childTokenLimitFormat = "The agent stopped after using %d of its %d token limit (PEEN_MAX_TURN_TOKENS)." //nolint:lll // One user-facing sentence.
)

// roundLimitError reports a run that used every tool round it is allowed. Its
// text is written for the person who reads the failure and holds no source
// location, so it can be shown and stored as it is.
type roundLimitError struct {
	text string
}

var _ publicerror.Public = (*roundLimitError)(nil)

func newRoundLimitError(limit int, isChild bool) *roundLimitError {
	format := rootRoundLimitFormat
	if isChild {
		format = childRoundLimitFormat
	}

	return &roundLimitError{text: fmt.Sprintf(format, limit)}
}

func (e *roundLimitError) Error() string {
	return e.text
}

// PublicMessage returns the text written for the reader.
func (e *roundLimitError) PublicMessage() string {
	return e.text
}

// Unwrap matches the round limit sentinel and its class. It keeps matching
// elelem.ErrMaxRoundsExceeded, which the same condition raised before.
func (e *roundLimitError) Unwrap() error {
	return errors.Join(
		session.ErrTurnRoundLimit,
		commerr.ErrOutOfRange,
		elelem.ErrMaxRoundsExceeded,
	)
}

// tokenLimitError reports a run whose rounds billed the turn token limit.
type tokenLimitError struct {
	text string
}

var _ publicerror.Public = (*tokenLimitError)(nil)

func newTokenLimitError(used, limit int64, isChild bool) *tokenLimitError {
	format := rootTokenLimitFormat
	if isChild {
		format = childTokenLimitFormat
	}

	return &tokenLimitError{text: fmt.Sprintf(format, used, limit)}
}

func (e *tokenLimitError) Error() string {
	return e.text
}

// PublicMessage returns the text written for the reader.
func (e *tokenLimitError) PublicMessage() string {
	return e.text
}

// Unwrap matches the token limit sentinel and its class.
func (e *tokenLimitError) Unwrap() error {
	return errors.Join(session.ErrTurnTokenLimit, commerr.ErrOutOfRange)
}

// workflowChildView is the part of a saved child bootstrap that names the
// child's own round limit.
type workflowChildView struct {
	MaxRounds int `json:"maxRounds"`
}

// workflowLimitView is the part of a saved conversation the turn limits read.
// Decoding only this keeps a limit check from loading the whole history.
type workflowLimitView struct {
	Usage  elelem.Usage             `json:"usage"`
	Limits *workflowExecutionLimits `json:"limits,omitempty"`
	Child  *workflowChildView       `json:"child,omitempty"`
}

// workflowRoundLimitError builds the error for an execution that ran out of
// rounds. A child reports the limit it was launched with, not the rounds it
// had left when this run resumed.
func workflowRoundLimitError(input workflowExecutionInput) *roundLimitError {
	var view workflowLimitView
	if err := json.Unmarshal(input.Context, &view); err != nil {
		return newRoundLimitError(input.MaxRounds, false)
	}

	if view.Child != nil && view.Child.MaxRounds > 0 {
		return newRoundLimitError(view.Child.MaxRounds, true)
	}

	return newRoundLimitError(input.MaxRounds, view.Child != nil)
}

// checkWorkflowTurnTokens refuses another model round once the rounds so far
// have billed the run's token limit. It runs in the workflow before the round
// is scheduled, so the refusal is the run's own failure rather than an
// activity error that the workflow would retry.
func checkWorkflowTurnTokens(input workflowExecutionInput) error {
	var view workflowLimitView
	if err := json.Unmarshal(input.Context, &view); err != nil {
		return ctxerrors.Wrap(err, "decode workflow turn usage")
	}

	return turnTokenLimitFor(view.Usage, view.Limits, view.Child != nil)
}

// turnTokenLimitFor returns the token limit error once usage has billed the
// saved limit, and nil while a round may still run or no limit is set.
func turnTokenLimitFor(
	usage elelem.Usage,
	limits *workflowExecutionLimits,
	isChild bool,
) error {
	if limits == nil || limits.MaxTurnTokens <= 0 {
		return nil
	}

	used := usage.BilledTotalTokens()
	if used < limits.MaxTurnTokens {
		return nil
	}

	return ctxerrors.Wrap(
		newTokenLimitError(used, limits.MaxTurnTokens, isChild),
		"check turn token limit",
	)
}
