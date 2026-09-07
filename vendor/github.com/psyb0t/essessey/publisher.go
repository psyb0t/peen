package essessey

import (
	"context"
	"encoding/json"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

// logFieldIndex names the content-block index in structured log fields.
const logFieldIndex = "index"

// Publisher emits protocol events to a Sink. It is constructed per stream with
// the request context so the streamer helpers can call the Send* methods
// without threading ctx through every call.
//
//nolint:containedctx // per-request publisher; ctx bounds the stream
type Publisher struct {
	ctx  context.Context
	sink Sink
}

// NewPublisher builds a Publisher that writes to sink for the lifetime of ctx.
//
// sink decides the delivery — framed bytes for a stream, a native message for
// NATS or WebSocket. Nothing below this line knows or cares which.
func NewPublisher(ctx context.Context, sink Sink) *Publisher {
	return &Publisher{ctx: ctx, sink: sink}
}

// Publish marshals data and emits it as an event of eventType. Every event gets
// a DEBUG log line (event kind + block index/type where applicable) so the
// wire-level flow of a turn is reconstructable from logs alone. For
// text/thinking/tool-result deltas ONLY the content LENGTH is logged, never the
// content itself — this is the single choke point all SendXxx helpers funnel
// through, so instrumenting it here covers every event without a duplicated log
// call in each helper, and keeps model output / tool-result payloads (which may
// carry sensitive data) out of logs at high frequency.
func (p *Publisher) Publish(eventType EventType, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return ctxerrors.Wrap(err, "marshal event data")
	}

	event := Event{
		Event: eventType,
		Data:  json.RawMessage(jsonData),
	}

	logEvent(p.ctx, eventType, data)

	if err := p.sink.Emit(p.ctx, event); err != nil {
		return ctxerrors.Wrap(err, "emit event")
	}

	return nil
}

// logEvent emits one DEBUG line describing eventType + data's block index and
// block type (when the concrete shape carries them), plus a LENGTH-only field
// for any delta payload — never the payload text itself.
func logEvent(ctx context.Context, eventType EventType, data any) {
	fields := append([]any{"event", eventType}, logFields(data)...)

	ctxscope.GetLogger(ctx).Debug("essessey event", fields...)
}

// logFields returns the fields that vary by payload shape.
//
// The cases cannot be collapsed with `case A, B, C:` even where the bodies read
// identically: a multi-type case binds the value at the switch expression's
// static type — `any` — so `d.Index` stops resolving. The three block-start
// payloads also carry three DIFFERENT ContentBlock types, so `d.ContentBlock`
// is a different field on each. Returning the fields keeps one Debug call site
// and reduces each case to the line that is genuinely per-shape.
func logFields(data any) []any {
	switch d := data.(type) {
	case ContentBlockStartData:
		return blockStartFields(d.Index, d.ContentBlock.Type)
	case ContentBlockStartToolUseData:
		return blockStartFields(d.Index, d.ContentBlock.Type)
	case ContentBlockStartToolResultData:
		return blockStartFields(d.Index, d.ContentBlock.Type)
	case ContentBlockDeltaData:
		return blockDeltaFields(d.Index, d.Delta.Type, len(d.Delta.Text))
	case ContentBlockDeltaToolInputData:
		return blockDeltaFields(
			d.Index, d.Delta.Type, len(d.Delta.PartialJSON),
		)
	case ContentBlockDeltaToolResultData:
		return blockDeltaFields(d.Index, d.Delta.Type, len(d.Delta.Text))
	case ContentBlockStopData:
		return []any{logFieldIndex, d.Index}
	case MessageDeltaData:
		return []any{
			"stop_reason", d.Delta.StopReason,
			"output_tokens", d.Usage.OutputTokens,
		}
	default:
		return nil
	}
}

func blockStartFields(index int, blockType ContentBlockType) []any {
	return []any{logFieldIndex, index, "block_type", blockType}
}

// blockDeltaFields takes the payload LENGTH, never the payload — a delta
// carries model output and tool arguments, which do not belong in a log line.
func blockDeltaFields(
	index int,
	blockType ContentBlockType,
	deltaLen int,
) []any {
	return []any{
		logFieldIndex, index,
		"block_type", blockType,
		"delta_len", deltaLen,
	}
}

// SendMessageStart emits message_start, carrying the stream id so the client
// learns a newly-created stream's id from the stream itself.
//
// streamID is the caller's own identifier for whatever this stream represents —
// a conversation, a job, a document build. essessey never mints one and only
// echoes it back; it is also the natural key to retain the stream under in an
// EventStore, so the same value passed here goes to EventStore.SinkFor.
func (p *Publisher) SendMessageStart(
	msgID, streamID, model string,
) error {
	return p.Publish(
		EventTypeMessageStart,
		MessageStartData{
			Type: EventTypeMessageStart,
			Message: MessageMeta{
				ID:           msgID,
				StreamID:     streamID,
				Type:         MessageTypeMessage,
				Role:         RoleAssistant,
				Content:      []any{},
				Model:        model,
				StopReason:   nil,
				StopSequence: nil,
				Usage: UsageStart{
					InputTokens:  0,
					OutputTokens: 0,
				},
			},
		},
	)
}

// SendPing emits a ping keep-alive event.
func (p *Publisher) SendPing() error {
	return p.Publish(EventTypePing, PingData{Type: EventTypePing})
}

// SendContentBlockStartText opens a text content block at index.
func (p *Publisher) SendContentBlockStartText(index int) error {
	return p.Publish(
		EventTypeContentBlockStart,
		ContentBlockStartData{
			Type:  EventTypeContentBlockStart,
			Index: index,
			ContentBlock: ContentBlock{
				Type: ContentBlockTypeText,
				Text: "",
			},
		},
	)
}

// SendContentBlockDeltaText appends a text delta to the block at index.
func (p *Publisher) SendContentBlockDeltaText(index int, text string) error {
	return p.Publish(
		EventTypeContentBlockDelta,
		ContentBlockDeltaData{
			Type:  EventTypeContentBlockDelta,
			Index: index,
			Delta: TextDelta{
				Type: ContentBlockTypeTextDelta,
				Text: text,
			},
		},
	)
}

// SendContentBlockStartThinking opens a thinking (reasoning) content block at
// index. Reasoning streams as its own block type so the client renders it apart
// from the final answer text.
func (p *Publisher) SendContentBlockStartThinking(index int) error {
	return p.Publish(
		EventTypeContentBlockStart,
		ContentBlockStartData{
			Type:  EventTypeContentBlockStart,
			Index: index,
			ContentBlock: ContentBlock{
				Type: ContentBlockTypeThinking,
				Text: "",
			},
		},
	)
}

// SendContentBlockDeltaThinking appends a reasoning delta at index.
func (p *Publisher) SendContentBlockDeltaThinking(
	index int,
	text string,
) error {
	return p.Publish(
		EventTypeContentBlockDelta,
		ContentBlockDeltaData{
			Type:  EventTypeContentBlockDelta,
			Index: index,
			Delta: TextDelta{
				Type: ContentBlockTypeThinkingDelta,
				Text: text,
			},
		},
	)
}

// SendContentBlockStop closes the content block at index.
func (p *Publisher) SendContentBlockStop(index int) error {
	return p.Publish(
		EventTypeContentBlockStop,
		ContentBlockStopData{
			Type:  EventTypeContentBlockStop,
			Index: index,
		},
	)
}

// SendMessageDelta emits the trailing message_delta with stop reason + usage.
func (p *Publisher) SendMessageDelta(
	stopReason StopReason,
	outputTokens int,
) error {
	return p.Publish(
		EventTypeMessageDelta,
		MessageDeltaData{
			Type: EventTypeMessageDelta,
			Delta: MessageDeltaInfo{
				StopReason:   stopReason,
				StopSequence: nil,
			},
			Usage: UsageEnd{
				OutputTokens: outputTokens,
			},
		},
	)
}

// SendMessageStop emits the terminal message_stop event.
func (p *Publisher) SendMessageStop() error {
	return p.Publish(
		EventTypeMessageStop,
		MessageStopData{Type: EventTypeMessageStop},
	)
}

// SendToolUseStart opens a tool_use content block (name known, args pending).
func (p *Publisher) SendToolUseStart(
	index int,
	toolUseID, name string,
) error {
	return p.Publish(
		EventTypeContentBlockStart,
		ContentBlockStartToolUseData{
			Type:  EventTypeContentBlockStart,
			Index: index,
			ContentBlock: ToolUseBlock{
				Type:  ContentBlockTypeToolUse,
				ID:    toolUseID,
				Name:  name,
				Input: map[string]any{},
			},
		},
	)
}

// SendToolInputDelta streams the tool's partial input JSON at index.
func (p *Publisher) SendToolInputDelta(index int, inputJSON string) error {
	return p.Publish(
		EventTypeContentBlockDelta,
		ContentBlockDeltaToolInputData{
			Type:  EventTypeContentBlockDelta,
			Index: index,
			Delta: InputJSONDelta{
				Type:        ContentBlockTypeInputJSON,
				PartialJSON: inputJSON,
			},
		},
	)
}

// SendToolResultStart opens a tool_result content block for toolUseID.
func (p *Publisher) SendToolResultStart(
	index int,
	toolUseID string,
	isError bool,
) error {
	return p.Publish(
		EventTypeContentBlockStart,
		ContentBlockStartToolResultData{
			Type:  EventTypeContentBlockStart,
			Index: index,
			ContentBlock: ToolResultBlock{
				Type:      ContentBlockTypeToolResult,
				ToolUseID: toolUseID,
				IsError:   isError,
			},
		},
	)
}

// SendToolResultDelta streams the tool result payload at index.
func (p *Publisher) SendToolResultDelta(index int, text string) error {
	return p.Publish(
		EventTypeContentBlockDelta,
		ContentBlockDeltaToolResultData{
			Type:  EventTypeContentBlockDelta,
			Index: index,
			Delta: ToolResultDelta{
				Type: ContentBlockTypeJSONPartial,
				Text: text,
			},
		},
	)
}

// SendStreamPreamble emits message_start + ping to open a stream.
func (p *Publisher) SendStreamPreamble(
	msgID, streamID, model string,
) error {
	if err := p.SendMessageStart(msgID, streamID, model); err != nil {
		return ctxerrors.Wrap(err, "send stream preamble")
	}

	return p.SendPing()
}

// SendStreamEpilogue emits message_delta + message_stop to close a stream,
// carrying stopReason so the client knows WHY the stream ended (a clean answer,
// a tool request, or a truncation like a token cap) instead of always being
// told end_turn regardless of what actually happened.
func (p *Publisher) SendStreamEpilogue(
	stopReason StopReason,
	outputTokens int,
) error {
	if err := p.SendMessageDelta(stopReason, outputTokens); err != nil {
		return ctxerrors.Wrap(err, "send stream epilogue")
	}

	return p.SendMessageStop()
}

// SendToolUseBlock emits a full tool_use block (start + input delta + stop).
func (p *Publisher) SendToolUseBlock(
	index int,
	toolUseID, name, inputJSON string,
) error {
	if err := p.SendToolUseStart(index, toolUseID, name); err != nil {
		return ctxerrors.Wrap(err, "start tool use block")
	}

	if err := p.SendToolInputDelta(index, inputJSON); err != nil {
		return ctxerrors.Wrap(err, "send tool use input")
	}

	return p.SendContentBlockStop(index)
}

// SendToolResultBlock emits a full tool_result block (start + delta + stop).
func (p *Publisher) SendToolResultBlock(
	index int,
	toolUseID, resultText string,
	isError bool,
) error {
	if err := p.SendToolResultStart(index, toolUseID, isError); err != nil {
		return ctxerrors.Wrap(err, "start tool result block")
	}

	if err := p.SendToolResultDelta(index, resultText); err != nil {
		return ctxerrors.Wrap(err, "send tool result text")
	}

	return p.SendContentBlockStop(index)
}
