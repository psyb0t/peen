package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
)

const (
	repeatWarningThreshold = 3
	repeatRefusalThreshold = 5
	repeatWarning          = "[Repeated call: the same call returned the same result several times. Use what you have or change approach.]" //nolint:lll // One model-facing line.
	repeatRefusal          = "This exact call already returned " +
		"the same result " +
		"several times. Peen did not run it again. Use that earlier result " +
		"or change the arguments or approach."
)

type workflowRepeatCount struct {
	Hash    string         `json:"hash"`
	Count   int            `json:"count"`
	Results map[string]int `json:"results,omitempty"`
}

type workflowRepeatResult struct {
	Key     string `json:"key"`
	Refused bool   `json:"refused,omitempty"`
	workflowRepeatCount
}

// Polling observes state that may change without a different call.
func repeatExemptTool(name string) bool {
	switch name {
	case toolNameWaitJob, toolNameReadJobOutput, toolNameListJobs,
		toolNameReadExecution, toolNameSearchExecutions, toolNameReadMetrics,
		toolNameAsk, toolNameWaitMessages:
		return true
	}

	return false
}

func repeatCallKey(call elelem.ToolCall) string {
	var value any

	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.UseNumber()

	if decoder.Decode(&value) != nil {
		return ""
	}

	canonical, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return repeatHash(call.Name + "\n" + string(canonical))
}

func repeatHash(text string) string {
	hash := sha256.Sum256([]byte(text))

	return hex.EncodeToString(hash[:])
}

func repeatCheckedWorkflowTool(
	tool elelem.Tool,
	input workflowToolInput,
) (elelem.Tool, *workflowRepeatResult) {
	if repeatExemptTool(tool.Name) || tool.Handler == nil {
		return tool, nil
	}

	var conversation workflowConversation
	if json.Unmarshal(input.Round.Execution.Context, &conversation) != nil {
		return tool, nil
	}

	key := repeatCallKey(input.Call)
	if key == "" {
		return tool, nil
	}

	previous := conversation.Repeats[key]
	record := &workflowRepeatResult{Key: key}
	tool.Handler = repeatToolHandler(tool.Handler, previous, record)

	return tool, record
}

func repeatToolHandler(
	handler elelem.ToolHandler,
	previous workflowRepeatCount,
	record *workflowRepeatResult,
) elelem.ToolHandler {
	return func(ctx context.Context, call elelem.ToolInput) (
		elelem.ToolResult, error,
	) {
		if previous.Count >= repeatRefusalThreshold-1 {
			record.Refused = true

			return elelem.ToolResult{}, ctxerrors.Wrap(
				commerr.ErrInvalidState, repeatRefusal)
		}

		return handler(ctx, call)
	}
}

func finishWorkflowRepeat(
	ctx context.Context,
	input workflowToolInput,
	outcome workflowToolResult,
	options workflowToolOptions,
) (workflowToolResult, error) {
	outcome, err := boundedWorkflowToolResult(ctx, outcome, options)
	if err != nil {
		return outcome, ctxerrors.Wrap(err, "bound repeated tool result")
	}

	record := completedRepeatRecord(input, outcome.Repeat)
	if record == nil || record.Refused {
		return outcome, nil
	}

	outcome.Repeat = record

	var conversation workflowConversation

	err = json.Unmarshal(input.Round.Execution.Context, &conversation)
	if err != nil {
		return outcome, ctxerrors.Wrap(err, "restore repeat result counts")
	}

	text := outcome.Result.Content
	if outcome.Result.IsError {
		text = "error\n" + text
	}

	previous := conversation.Repeats[record.Key]
	record.Hash = repeatHash(text)
	record.Count = previous.Results[record.Hash] + 1

	if previous.Results == nil && previous.Hash == record.Hash {
		record.Count = previous.Count + 1
	}

	if record.Count != repeatWarningThreshold {
		return outcome, nil
	}

	outcome.Result.Content = appendRepeatWarning(outcome.Result.Content)

	return boundRepeatWarning(ctx, outcome, options)
}

func completedRepeatRecord(
	input workflowToolInput,
	record *workflowRepeatResult,
) *workflowRepeatResult {
	if record != nil || repeatExemptTool(input.Call.Name) {
		return record
	}

	key := repeatCallKey(input.Call)
	if key == "" {
		return nil
	}

	return &workflowRepeatResult{Key: key}
}

func boundRepeatWarning(
	ctx context.Context,
	outcome workflowToolResult,
	options workflowToolOptions,
) (workflowToolResult, error) {
	opening, inner, closing, wrapped := splitToolOutput(outcome.Result.Content)
	if !wrapped || options.MaxResultTokens <= 0 {
		return boundedWorkflowToolResult(ctx, outcome, options)
	}

	counter := options.Counter
	if counter == nil {
		counter = elelem.DefaultTokenCounter()
	}

	content, err := cutToolResult(ctx, counter, opening+inner+closing,
		options.MaxResultTokens)
	if err != nil {
		return outcome, ctxerrors.Wrap(err, "fit repeated call warning")
	}

	outcome.Result.Content = content

	return outcome, nil
}

func appendRepeatWarning(content string) string {
	opening, inner, closing, wrapped := splitToolOutput(content)
	if wrapped {
		return opening + appendRepeatWarning(inner) + closing
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &fields) == nil && fields != nil {
		warning, err := json.Marshal(repeatWarning)
		if err != nil {
			return content + "\n" + repeatWarning
		}

		fields["repeatWarning"] = warning

		encoded, err := json.Marshal(fields)
		if err == nil {
			return string(encoded)
		}
	}

	return content + "\n" + repeatWarning
}

func retainWorkflowRepeat(
	conversation *workflowConversation,
	record *workflowRepeatResult,
) {
	if record == nil || record.Count == 0 {
		return
	}

	if conversation.Repeats == nil {
		conversation.Repeats = make(map[string]workflowRepeatCount)
	}

	previous := conversation.Repeats[record.Key]
	if previous.Results == nil {
		previous.Results = make(map[string]int)
		if previous.Count != 0 {
			previous.Results[previous.Hash] = previous.Count
		}
	}

	previous.Results[record.Hash]++
	previous.Hash, previous.Count = record.Hash, previous.Results[record.Hash]

	conversation.Repeats[record.Key] = previous
}

func workflowRepeatBatchEnd(
	round workflowRoundInput,
	calls []elelem.ToolCall,
	start int,
) int {
	end := min(start+round.Execution.MaxConcurrentTools, len(calls))

	var conversation workflowConversation
	if json.Unmarshal(round.Execution.Context, &conversation) != nil ||
		conversation.Limits == nil || !conversation.Limits.CompactTools {
		return end
	}

	seen := make(map[string]bool)

	for index := start; index < end; index++ {
		if repeatExemptTool(calls[index].Name) {
			continue
		}

		key := repeatCallKey(calls[index])
		if key == "" {
			continue
		}

		if seen[key] {
			return index
		}

		seen[key] = true
	}

	return end
}

func advanceWorkflowRepeatRound(
	round workflowRoundInput,
	results []workflowToolResult,
) (workflowRoundInput, error) {
	var conversation workflowConversation

	err := json.Unmarshal(round.Execution.Context, &conversation)
	if err != nil {
		return round, ctxerrors.Wrap(err, "decode repeat batch context")
	}

	if conversation.Limits == nil || !conversation.Limits.CompactTools {
		return round, nil
	}

	for _, result := range results {
		retainWorkflowRepeat(&conversation, result.Repeat)
	}

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return round, ctxerrors.Wrap(err, "encode repeat batch context")
	}

	round.Execution.Context = encoded

	return round, nil
}
