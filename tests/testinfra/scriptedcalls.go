package testinfra

import "net/http"

// writeScriptedCalls answers a request of a Calls-mode script. The round is
// the number of tool results after the script's user message, so the same
// script keeps working when a queued message or a skill lands in between. A
// request without the script's user message is not part of the script, such
// as a child agent's, and gets the default text completion.
func (m *openAIModelsMock) writeScriptedCalls(
	writer http.ResponseWriter,
	request providerCompletionRequest,
	script *ScriptedToolTurn,
) {
	results, anchored := request.toolResultsAfter(script.UserMessage)
	if !anchored {
		m.writeCompletion(writer)

		return
	}

	round := len(results)

	var (
		stream []byte
		err    error
	)

	if round < len(script.Calls) {
		call := script.Calls[round]

		arguments := call.Arguments
		if call.ArgumentsFrom != nil {
			arguments = call.ArgumentsFrom(results)
		}

		stream, err = openAIToolCallStream(
			ScriptedCallID(round),
			call.Name,
			arguments,
		)
	} else {
		stream, err = openAITextCompletionStream(script.FinalAnswer)
	}

	if err != nil {
		http.Error(
			writer,
			"failed to encode mock scripted completion response",
			http.StatusInternalServerError,
		)

		return
	}

	writer.Header().Set("Content-Type", eventStreamMediaType)

	if _, err := writer.Write(stream); err != nil {
		return
	}
}

// toolResultsAfter returns the content of the tool messages after the last
// user message that equals userMessage, and whether such a message exists.
func (r providerCompletionRequest) toolResultsAfter(
	userMessage string,
) ([]string, bool) {
	start := -1

	for index, message := range r.Messages {
		if message.Role != providerRoleUser {
			continue
		}

		if message.contentText() == userMessage {
			start = index + 1
		}
	}

	if start < 0 {
		return nil, false
	}

	results := make([]string, 0)

	for _, message := range r.Messages[start:] {
		if message.Role == providerRoleTool {
			results = append(results, message.contentText())
		}
	}

	return results, true
}
