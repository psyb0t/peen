package testinfra

import (
	"encoding/json"
	"strconv"

	"github.com/psyb0t/ctxerrors"
)

// scriptedCompletionStream picks the scripted turn's next move purely from
// how many tool-role messages the incoming transcript already carries.
func scriptedCompletionStream(
	round int,
	script *ScriptedToolTurn,
) ([]byte, error) {
	switch round {
	case scriptedRoundReadFile:
		return openAIToolCallStream(
			ScriptedCallIDReadFile,
			scriptedToolNameReadFile,
			script.ReadFileArguments,
		)
	case scriptedRoundEditFile:
		if script.ApplyPatchArguments != nil {
			return openAIToolCallStream(
				ScriptedCallIDApplyPatch,
				scriptedToolNameApplyPatch,
				script.ApplyPatchArguments,
			)
		}

		return openAIToolCallStream(
			ScriptedCallIDEditFile,
			scriptedToolNameEditFile,
			script.EditFileArguments,
		)
	case scriptedRoundRunCommand:
		return openAIToolCallStream(
			ScriptedCallIDRunCommand,
			scriptedToolNameRunCommand,
			script.RunCommandArguments,
		)
	default:
		return openAITextCompletionStream(script.FinalAnswer)
	}
}

func openAICompletionStream(sequence int64) ([]byte, error) {
	text := providerResponsePrefix + strconv.FormatInt(sequence, 10)

	return openAIReasoningCompletionStream(DefaultProviderReasoning, text)
}

// openAIReasoningCompletionStream renders visible reasoning followed by text,
// using OpenAI-compatible reasoning_content wire data.
func openAIReasoningCompletionStream(
	reasoning string,
	text string,
) ([]byte, error) {
	chunks := []map[string]any{
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex: 0,
			openAIFieldDelta: map[string]any{
				openAIDeltaFieldRole:        providerRoleAssistant,
				openAIDeltaReasoningContent: reasoning,
			},
			openAIFieldFinishReason: nil,
		}),
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex: 0,
			openAIFieldDelta: map[string]any{
				"content": text,
			},
			openAIFieldFinishReason: nil,
		}),
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex:        0,
			openAIFieldDelta:        map[string]any{},
			openAIFieldFinishReason: openAIFinishReasonStop,
		}),
	}

	return encodeOpenAIStream(chunks)
}

// openAITextCompletionStream renders a plain two-chunk assistant text
// completion: one chunk carrying the full text, one closing chunk with
// finish_reason stop.
func openAITextCompletionStream(text string) ([]byte, error) {
	chunks := []map[string]any{
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex: 0,
			openAIFieldDelta: map[string]any{
				openAIDeltaFieldRole: providerRoleAssistant,
				"content":            text,
			},
			openAIFieldFinishReason: nil,
		}),
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex:        0,
			openAIFieldDelta:        map[string]any{},
			openAIFieldFinishReason: openAIFinishReasonStop,
		}),
	}

	return encodeOpenAIStream(chunks)
}

// openAIToolCallStream renders a two-chunk tool-call completion: one chunk
// carrying the full function name and arguments at tool_calls[0], one
// closing chunk with finish_reason tool_calls. A real provider may split
// the arguments across several deltas; the engine accumulates by index
// either way, so one full delta is a faithful, simpler stand-in.
func openAIToolCallStream(
	callID string,
	name string,
	arguments map[string]any,
) ([]byte, error) {
	argumentsJSON, err := json.Marshal(arguments)
	if err != nil {
		return nil, ctxerrors.Wrap(
			err,
			"marshal scripted tool call arguments",
		)
	}

	chunks := []map[string]any{
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex: 0,
			openAIFieldDelta: map[string]any{
				openAIDeltaFieldRole: providerRoleAssistant,
				openAIDeltaFieldToolCalls: []map[string]any{{
					openAIFieldIndex: 0,
					"id":             callID,
					"type":           openAIToolCallType,
					"function": map[string]any{
						"name":      name,
						"arguments": string(argumentsJSON),
					},
				}},
			},
			openAIFieldFinishReason: nil,
		}),
		openAIChunkEnvelope(map[string]any{
			openAIFieldIndex:        0,
			openAIFieldDelta:        map[string]any{},
			openAIFieldFinishReason: openAIFinishReasonToolCalls,
		}),
	}

	return encodeOpenAIStream(chunks)
}

// openAIChunkEnvelope wraps one choice in the chunk header every mock
// completion event shares.
func openAIChunkEnvelope(choice map[string]any) map[string]any {
	return map[string]any{
		"id":                providerCompletionID,
		providerObjectField: providerChunkObjectType,
		"created":           providerCreatedAt,
		"model":             providerModel,
		"choices":           []map[string]any{choice},
	}
}

// encodeOpenAIStream renders chunks as SSE data events terminated by the
// OpenAI [DONE] sentinel.
func encodeOpenAIStream(chunks []map[string]any) ([]byte, error) {
	stream := make([]byte, 0, len(openAIDoneEvent))

	for _, chunk := range chunks {
		var err error

		stream, err = appendOpenAIStreamEvent(stream, chunk)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "encode mock OpenAI stream event")
		}
	}

	return append(stream, openAIDoneEvent...), nil
}

func appendOpenAIStreamEvent(
	stream []byte,
	payload map[string]any,
) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "marshal mock OpenAI stream event")
	}

	stream = append(stream, openAIDataPrefix...)
	stream = append(stream, encoded...)

	return append(stream, '\n', '\n'), nil
}
