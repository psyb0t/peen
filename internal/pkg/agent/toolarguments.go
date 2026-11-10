package agent

import (
	"encoding/json"
	"slices"

	"github.com/psyb0t/elelem"
)

// validToolCalls returns calls with every argument that is not valid JSON
// replaced by an empty object. Elelem keeps a model's malformed arguments, and
// a reply cut off at the output limit can end inside a call, but raw JSON that
// does not parse cannot be saved. The tool then refuses the empty arguments,
// so the model still learns its call failed.
func validToolCalls(calls []elelem.ToolCall) []elelem.ToolCall {
	invalid := slices.IndexFunc(calls, func(call elelem.ToolCall) bool {
		return !json.Valid(call.Arguments)
	})
	if invalid < 0 {
		return calls
	}

	valid := slices.Clone(calls)
	for index := range valid {
		if !json.Valid(valid[index].Arguments) {
			valid[index].Arguments = json.RawMessage(emptyToolArguments)
		}
	}

	return valid
}
