package elelemstream

import (
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
)

// MapStopReason maps elelem's finish reason and whether the round produced
// tool calls onto essessey's StopReason.
//
// A truncated finish always wins over tool calls: the client must not treat
// a cut-off answer as a clean tool-use round just because calls happened to
// be present in it.
func MapStopReason(
	finishReason elelem.FinishReason,
	hasToolCalls bool,
) essessey.StopReason {
	if finishReason.IsTruncated() {
		return essessey.StopReasonMaxTokens
	}

	if hasToolCalls {
		return essessey.StopReasonToolUse
	}

	return essessey.StopReasonEndTurn
}
