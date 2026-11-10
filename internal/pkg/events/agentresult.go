package events

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
)

const (
	// AgentFinishedSummaryFormat takes the agent name and the run ID.
	AgentFinishedSummaryFormat = "agent %q run %s finished"
	// AgentFailedSummaryFormat takes the agent name, the run ID, and the
	// terminal run state.
	AgentFailedSummaryFormat = "agent %q run %s did not finish (%s)"
	// AgentResultTruncatedNote ends a final answer cut to fit the event data
	// bound. The run record keeps the whole text.
	//nolint:lll // One message string.
	AgentResultTruncatedNote = "\n\n[cut to fit the event, the whole answer is in the agent run record]"
	// AgentFailureDetailBytes bounds the failure text an agent.failed event
	// carries.
	AgentFailureDetailBytes = 1024
	// agentResultCutAttempts bounds how often a final answer is shortened
	// when JSON escaping keeps it over the event data bound.
	agentResultCutAttempts = 8
)

// AgentResultData is the structured payload of agent.finished and
// agent.failed notices. Duration is DurationMs rendered as a Go duration
// string. Response is the child's final answer and ResponseTruncated says it
// was cut to fit.
type AgentResultData struct {
	RunID             uuid.UUID `json:"runId"`
	AgentName         string    `json:"agentName"`
	State             string    `json:"state"`
	DurationMs        int64     `json:"durationMs"`
	Duration          string    `json:"duration"`
	Response          string    `json:"response,omitempty"`
	ResponseTruncated bool      `json:"responseTruncated,omitempty"`
	FailureDetail     string    `json:"failureDetail,omitempty"`
}

// EncodeAgentResultData fits data into maxBytes by cutting the final answer
// and marking it cut. JSON escaping can make the cut text larger than its
// byte count, so the cut repeats a bounded number of times before the answer
// is left out altogether.
func EncodeAgentResultData(
	data AgentResultData,
	maxBytes int,
) (json.RawMessage, error) {
	whole := data.Response
	kept := len(whole)

	for range agentResultCutAttempts {
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "encode agent event data")
		}

		if len(encoded) <= maxBytes {
			return encoded, nil
		}

		// Shrink in proportion to the overshoot, so heavily escaped text
		// keeps a useful part instead of dropping to nothing.
		kept = max(kept*maxBytes/len(encoded)-len(AgentResultTruncatedNote), 0)
		data.Response = TruncateToRuneBoundary(whole, kept) +
			AgentResultTruncatedNote
		data.ResponseTruncated = true
	}

	data.Response = ""
	data.ResponseTruncated = whole != ""

	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode agent event data")
	}

	return encoded, nil
}
