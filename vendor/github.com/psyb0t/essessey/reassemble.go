package essessey

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

// ParsedStream is the structured reconstruction of a full streamed turn.
type ParsedStream struct {
	StreamID   string
	Thinking   string
	Text       string
	ToolNames  []string
	Tools      []ToolCall
	Executions []ToolExecution
	Timeline   []TimelineItem
	Error      string
}

// Reassemble drains src and reconstructs a full streamed turn: accumulated
// reasoning, text, tool_use blocks matched against their tool_result blocks by
// content-block index, and an ordered timeline of all three.
//
// A malformed individual event is warn-logged and dropped rather than
// aborting reassembly — a single corrupted event degrades the result, not
// the caller. src.Next returning ErrNoMoreEvents ends the stream cleanly;
// the accumulated result is returned, never an error. Any other error from
// Next is recorded on the result's Error field and reassembly stops there,
// still returning whatever was accumulated so far.
func Reassemble(ctx context.Context, src Source) ParsedStream {
	r := &reassembler{
		tools:         map[int]*inflightTool{},
		results:       map[int]*inflightResult{},
		thinkingIndex: -1,
		textIndex:     -1,
	}

	return r.reassemble(ctx, src)
}

// inflightTool is one tool_use block in progress.
type inflightTool struct {
	name      string
	toolUseID string
	params    []string
}

// inflightResult mirrors inflightTool for tool_result blocks.
type inflightResult struct {
	toolUseID string
	parts     []string
}

type reassembler struct {
	result ParsedStream

	curTextParts []string
	allTextParts []string

	curThinkingParts []string
	allThinkingParts []string

	// Parallel tools: N starts back-to-back, then N delta+stop pairs,
	// keyed by content-block index so each delta/stop routes to the
	// right block.
	tools   map[int]*inflightTool
	results map[int]*inflightResult

	// thinkingIndex and textIndex are the currently open reasoning and text
	// blocks, or -1 when that type has no open block.
	thinkingIndex int
	textIndex     int
}

func (r *reassembler) reassemble(
	ctx context.Context,
	src Source,
) ParsedStream {
	for {
		ev, err := src.Next(ctx)
		if err != nil {
			if errors.Is(err, ErrNoMoreEvents) {
				break
			}

			r.result.Error = ctxerrors.Wrap(
				err, "read next event",
			).Error()

			break
		}

		r.handleEvent(ctx, ev.Event, ev.Data)

		if ev.Event == EventTypeMessageStop {
			return r.result
		}
	}

	r.finalize(ctx)

	return r.result
}

func (r *reassembler) handleEvent(
	ctx context.Context,
	eventType EventType,
	data json.RawMessage,
) {
	switch eventType {
	case EventTypeMessageStart:
		r.onMessageStart(ctx, data)
	case EventTypeContentBlockStart:
		r.onContentBlockStart(ctx, data)
	case EventTypeContentBlockDelta:
		r.onContentBlockDelta(ctx, data)
	case EventTypeContentBlockStop:
		r.onContentBlockStop(ctx, data)
	case EventTypeMessageStop:
		r.finalize(ctx)
	}
}

func (r *reassembler) onMessageStart(
	ctx context.Context,
	data json.RawMessage,
) {
	var msg MessageStartData
	if err := json.Unmarshal(data, &msg); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"reassemble: malformed message_start, dropping event",
			"err", err,
			"reason", "malformed_event",
		)

		return
	}

	r.result.StreamID = msg.Message.StreamID
}

func (r *reassembler) onContentBlockStart(
	ctx context.Context,
	data json.RawMessage,
) {
	logger := ctxscope.GetLogger(ctx)

	// Need the index up front; never silently route to 0 — it would
	// collide with a real block at index 0 in the text-block fallthrough
	// below.
	var meta ContentBlockStopData
	if err := json.Unmarshal(data, &meta); err != nil {
		logger.Warn(
			"reassemble: malformed content_block_start, dropping event",
			"err", err,
			"reason", "malformed_event",
		)

		return
	}

	var toolUse ContentBlockStartToolUseData
	if json.Unmarshal(data, &toolUse) == nil &&
		toolUse.ContentBlock.Type == ContentBlockTypeToolUse {
		r.closeTextBlock()
		r.closeThinkingBlock()
		r.result.ToolNames = append(
			r.result.ToolNames,
			toolUse.ContentBlock.Name,
		)
		r.tools[toolUse.Index] = &inflightTool{
			name:      toolUse.ContentBlock.Name,
			toolUseID: toolUse.ContentBlock.ID,
		}

		return
	}

	var toolResult ContentBlockStartToolResultData
	if json.Unmarshal(data, &toolResult) == nil &&
		toolResult.ContentBlock.Type == ContentBlockTypeToolResult {
		r.closeTextBlock()
		r.closeThinkingBlock()
		r.results[toolResult.Index] = &inflightResult{
			toolUseID: toolResult.ContentBlock.ToolUseID,
		}

		return
	}

	r.startTextThinkingOrUnknown(ctx, data, meta.Index)
}

// startTextThinkingOrUnknown routes a non-tool content_block_start to the
// matching text or reasoning channel, recording its index, or drops it as
// unknown.
func (r *reassembler) startTextThinkingOrUnknown(
	ctx context.Context,
	data json.RawMessage,
	index int,
) {
	var generic struct {
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"` //nolint:tagliatelle // SSE wire format
	}

	if err := json.Unmarshal(data, &generic); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"reassemble: malformed content_block, dropping event",
			"err", err,
			"reason", "malformed_event",
		)

		return
	}

	switch generic.ContentBlock.Type {
	case ContentBlockTypeText, "":
		r.closeThinkingBlock()
		r.closeTextBlock()
		r.textIndex = index
	case ContentBlockTypeThinking:
		r.closeTextBlock()
		r.closeThinkingBlock()
		r.thinkingIndex = index
	default:
		ctxscope.GetLogger(ctx).Warn(
			"reassemble: unknown content_block type, dropping event",
			"type", generic.ContentBlock.Type,
			"index", index,
			"reason", "unknown_block_type",
		)
	}
}

func (r *reassembler) onContentBlockDelta(
	ctx context.Context,
	data json.RawMessage,
) {
	logger := ctxscope.GetLogger(ctx)

	var textDelta ContentBlockDeltaData
	if json.Unmarshal(data, &textDelta) == nil &&
		r.handleTextOrThinkingDelta(ctx, textDelta) {
		return
	}

	var inputDelta ContentBlockDeltaToolInputData
	if json.Unmarshal(data, &inputDelta) == nil &&
		inputDelta.Delta.Type == ContentBlockTypeInputJSON {
		if t, ok := r.tools[inputDelta.Index]; ok {
			t.params = append(t.params, inputDelta.Delta.PartialJSON)

			return
		}

		logger.Warn(
			"reassemble: tool_input delta has no in-flight tool",
			"index", inputDelta.Index,
			"reason", "orphan_delta",
		)

		return
	}

	var resultDelta ContentBlockDeltaToolResultData
	if json.Unmarshal(data, &resultDelta) == nil &&
		resultDelta.Delta.Type == ContentBlockTypeJSONPartial {
		if res, ok := r.results[resultDelta.Index]; ok {
			res.parts = append(res.parts, resultDelta.Delta.Text)

			return
		}

		logger.Warn(
			"reassemble: tool_result delta has no in-flight result",
			"index", resultDelta.Index,
			"reason", "orphan_delta",
		)

		return
	}

	logger.Warn(
		"reassemble: unknown content_block_delta shape, dropping event",
		"reason", "unknown_delta_shape",
	)
}

func (r *reassembler) handleTextOrThinkingDelta(
	ctx context.Context,
	delta ContentBlockDeltaData,
) bool {
	switch delta.Delta.Type {
	case ContentBlockTypeTextDelta:
		r.handleTextDelta(delta)

		return true
	case ContentBlockTypeThinkingDelta:
		r.handleThinkingDelta(ctx, delta)

		return true
	default:
		return false
	}
}

func (r *reassembler) handleTextDelta(
	delta ContentBlockDeltaData,
) {
	r.curTextParts = append(r.curTextParts, delta.Delta.Text)
}

func (r *reassembler) handleThinkingDelta(
	ctx context.Context,
	delta ContentBlockDeltaData,
) {
	if delta.Index == r.thinkingIndex {
		r.curThinkingParts = append(r.curThinkingParts, delta.Delta.Text)

		return
	}

	ctxscope.GetLogger(ctx).Warn(
		"reassemble: thinking delta has no in-flight thinking block",
		"index", delta.Index,
		"reason", "orphan_delta",
	)
}

func (r *reassembler) onContentBlockStop(
	ctx context.Context,
	data json.RawMessage,
) {
	var stop ContentBlockStopData
	if err := json.Unmarshal(data, &stop); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"reassemble: malformed content_block_stop, dropping event",
			"err", err,
			"reason", "malformed_event",
		)

		return
	}

	if t, ok := r.tools[stop.Index]; ok {
		r.finalizeTool(t)
		delete(r.tools, stop.Index)

		return
	}

	if res, ok := r.results[stop.Index]; ok {
		r.finalizeResult(ctx, res)
		delete(r.results, stop.Index)

		return
	}

	if stop.Index == r.textIndex {
		r.closeTextBlock()

		return
	}

	if stop.Index == r.thinkingIndex {
		r.closeThinkingBlock()

		return
	}

	ctxscope.GetLogger(ctx).Warn(
		"reassemble: orphan content_block_stop, no open block",
		"index", stop.Index,
		"reason", "orphan_stop",
	)
}

// flushAll finalizes any blocks that never received a content_block_stop
// (truncated stream).
func (r *reassembler) flushAll(ctx context.Context) {
	r.closeTextBlock()
	r.closeThinkingBlock()

	for idx, t := range r.tools {
		r.finalizeTool(t)
		delete(r.tools, idx)
	}

	for idx, res := range r.results {
		r.finalizeResult(ctx, res)
		delete(r.results, idx)
	}
}

func (r *reassembler) finalize(ctx context.Context) {
	r.flushAll(ctx)
	r.result.Thinking = strings.Join(r.allThinkingParts, "")
	r.result.Text = strings.Join(r.allTextParts, "")
}

func (r *reassembler) closeTextBlock() {
	r.flushTextSegment()
	r.textIndex = -1
}

func (r *reassembler) closeThinkingBlock() {
	r.flushThinkingSegment()
	r.thinkingIndex = -1
}

func (r *reassembler) flushTextSegment() {
	if len(r.curTextParts) == 0 {
		return
	}

	text := strings.Join(r.curTextParts, "")
	r.curTextParts = nil

	if text == "" {
		return
	}

	r.allTextParts = append(r.allTextParts, text)
	r.result.Timeline = append(r.result.Timeline, TimelineItem{
		Kind: TimelineKindText,
		Text: text,
	})
}

func (r *reassembler) flushThinkingSegment() {
	if len(r.curThinkingParts) == 0 {
		return
	}

	thinking := strings.Join(r.curThinkingParts, "")
	r.curThinkingParts = nil

	if thinking == "" {
		return
	}

	r.allThinkingParts = append(r.allThinkingParts, thinking)
	r.result.Timeline = append(r.result.Timeline, TimelineItem{
		Kind: TimelineKindThinking,
		Text: thinking,
	})
}

func (r *reassembler) finalizeTool(t *inflightTool) {
	rawJSON := strings.Join(t.params, "")
	if rawJSON == "" {
		rawJSON = emptyToolInput
	}

	r.result.Tools = append(r.result.Tools, ToolCall{
		Name:      t.name,
		Params:    json.RawMessage(rawJSON),
		ToolUseID: t.toolUseID,
	})
}

func (r *reassembler) finalizeResult(
	ctx context.Context,
	res *inflightResult,
) {
	resultText := strings.Join(res.parts, "")

	for i := range r.result.Tools {
		if r.result.Tools[i].ToolUseID != res.toolUseID {
			continue
		}

		exec := ToolExecution{
			Name:      r.result.Tools[i].Name,
			Params:    r.result.Tools[i].Params,
			Result:    resultText,
			ToolUseID: res.toolUseID,
		}

		r.result.Executions = append(r.result.Executions, exec)
		r.result.Timeline = append(r.result.Timeline, TimelineItem{
			Kind:      TimelineKindTool,
			Execution: &exec,
		})

		return
	}

	// No matching tool_use (truncated upstream / cross-stream rejoin).
	// Drop rather than fabricate an Execution missing Name/Params.
	ctxscope.GetLogger(ctx).Warn(
		"reassemble: tool_result has no matching tool_use",
		"tool_use_id", res.toolUseID,
		"result_len", len(resultText),
		"reason", "orphan_result",
	)
}
