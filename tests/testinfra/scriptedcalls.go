package testinfra

import (
	"net/http"
	"strings"
)

const (
	// taggedMessageOpen and taggedMessageClose frame a message one agent sent
	// another, such as a child's task.
	taggedMessageOpen  = "<message "
	taggedMessageClose = "\n</message>"
	taggedMessageBreak = "\n"

	// toolOutputOpen and toolOutputClose frame the untrusted-output wrapper
	// Peen puts around a tool result.
	toolOutputOpen  = "<toolOutput tool="
	toolOutputClose = "\n</toolOutput>\n"
)

// ToolOutputText returns the result text inside the <toolOutput> wrapper Peen
// puts around a tool result. Text without the wrapper is returned as it is.
func ToolOutputText(content string) string {
	if !strings.HasPrefix(content, toolOutputOpen) {
		return content
	}

	_, rest, hasBody := strings.Cut(content, taggedMessageBreak)
	end := strings.LastIndex(rest, toolOutputClose)

	if !hasBody || end < 0 {
		return content
	}

	return rest[:end]
}

// writeScriptedCalls answers a request of a Calls-mode script. The round is
// the number of tool results after the script's user message, so the same
// script keeps working when a queued message or a skill lands in between. A
// request without the script's user message follows the first child script
// whose task it carries, and otherwise gets the default text completion.
func (m *openAIModelsMock) writeScriptedCalls(
	writer http.ResponseWriter,
	request providerCompletionRequest,
	script *ScriptedToolTurn,
) {
	results, anchored := request.toolResultsAfter(script.UserMessage)
	if !anchored {
		m.writeChildScriptedCalls(writer, request, script)

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

// writeChildScriptedCalls answers a child agent's request from the child
// script whose task the request carries.
func (m *openAIModelsMock) writeChildScriptedCalls(
	writer http.ResponseWriter,
	request providerCompletionRequest,
	script *ScriptedToolTurn,
) {
	for index := range script.Children {
		child := &script.Children[index]

		_, isAnchored := request.toolResultsAfter(child.UserMessage)
		if isAnchored {
			m.writeScriptedCalls(writer, request, child)

			return
		}
	}

	m.writeCompletion(writer)
}

// toolResultsAfter returns the text of the tool results after the last user
// message that equals userMessage, or whose tagged body does, and whether
// such a message exists. Each result is taken out of its <toolOutput> wrapper,
// so scripted arguments read the tool's own output.
func (r providerCompletionRequest) toolResultsAfter(
	userMessage string,
) ([]string, bool) {
	start := -1

	for index, message := range r.Messages {
		if message.Role != providerRoleUser {
			continue
		}

		text := message.contentText()
		if text == userMessage || taggedMessageBody(text) == userMessage {
			start = index + 1
		}
	}

	if start < 0 {
		return nil, false
	}

	results := make([]string, 0)

	for _, message := range r.Messages[start:] {
		if message.Role == providerRoleTool {
			results = append(results, ToolOutputText(message.contentText()))
		}
	}

	return results, true
}

// taggedMessageBody returns the text inside a <message> block, or "" for any
// other text.
func taggedMessageBody(text string) string {
	if !strings.HasPrefix(text, taggedMessageOpen) {
		return ""
	}

	body, isClosed := strings.CutSuffix(text, taggedMessageClose)
	if !isClosed {
		return ""
	}

	_, body, hasBody := strings.Cut(body, taggedMessageBreak)
	if !hasBody {
		return ""
	}

	return body
}
