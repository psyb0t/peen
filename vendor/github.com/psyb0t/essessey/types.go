//nolint:tagliatelle // snake_case matches the streaming wire format
package essessey

import "encoding/json"

// EventType names one protocol event.
//
// On an SSE byte stream this is the `event:` line; over NATS it is the subject
// suffix; over WebSocket it is the envelope's type field. The NAME is the same
// everywhere — only the delivery differs.
type EventType = string

const (
	EventTypeMessageStart      EventType = "message_start"
	EventTypeContentBlockStart EventType = "content_block_start"
	EventTypePing              EventType = "ping"
	EventTypeContentBlockDelta EventType = "content_block_delta"
	EventTypeContentBlockStop  EventType = "content_block_stop"
	EventTypeMessageDelta      EventType = "message_delta"
	EventTypeMessageStop       EventType = "message_stop"
)

// MessageType tags a message envelope.
type MessageType = string

const MessageTypeMessage MessageType = "message"

// ContentBlockType is the type tag inside a content block or delta.
//
// Deliberately a string ALIAS, not a defined type: a producer may emit block
// types this package has never heard of, and a consumer must be able to name
// them without patching essessey. The constants below are the built-in set,
// not the permitted set.
type ContentBlockType = string

const (
	ContentBlockTypeText          ContentBlockType = "text"
	ContentBlockTypeTextDelta     ContentBlockType = "text_delta"
	ContentBlockTypeThinking      ContentBlockType = "thinking"
	ContentBlockTypeThinkingDelta ContentBlockType = "thinking_delta"
	ContentBlockTypeToolUse       ContentBlockType = "tool_use"
	ContentBlockTypeToolResult    ContentBlockType = "tool_result"
	ContentBlockTypeInputJSON     ContentBlockType = "input_json_delta"
	ContentBlockTypeJSONPartial   ContentBlockType = "json_partial"
)

// StopReason is why a message stopped.
type StopReason = string

const (
	StopReasonEndTurn StopReason = "end_turn"
	StopReasonToolUse StopReason = "tool_use"
	// StopReasonMaxTokens signals the model's response was cut off by a token
	// cap (upstream finish_reason "length") rather than finishing cleanly —
	// the client must NOT treat the turn as a complete answer.
	StopReasonMaxTokens StopReason = "max_tokens"
)

// Role is the author of a message.
//
// Declared here rather than imported so a wire-protocol package stays
// self-describing. An engine's Role (elelem's, say) is a different concept
// that happens to share values today, and the two must be free to diverge
// without breaking each other.
type Role = string

const RoleAssistant Role = "assistant"

// emptyToolInput is the JSON default when a tool_use block carries no input.
const emptyToolInput = "{}"

type MessageStartData struct {
	Type    EventType   `json:"type"`
	Message MessageMeta `json:"message"`
}

type MessageMeta struct {
	ID           string      `json:"id"`
	StreamID     string      `json:"stream_id"`
	Type         MessageType `json:"type"`
	Role         Role        `json:"role"`
	Content      []any       `json:"content"`
	Model        string      `json:"model"`
	StopReason   *StopReason `json:"stop_reason"`
	StopSequence *string     `json:"stop_sequence"`
	Usage        UsageStart  `json:"usage"`
}

type UsageStart struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type ContentBlockStartData struct {
	Type         EventType    `json:"type"`
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
}

type ContentBlock struct {
	Type ContentBlockType `json:"type"`
	Text string           `json:"text,omitempty"`
}

type PingData struct {
	Type EventType `json:"type"`
}

type ContentBlockDeltaData struct {
	Type  EventType `json:"type"`
	Index int       `json:"index"`
	Delta TextDelta `json:"delta"`
}

type TextDelta struct {
	Type ContentBlockType `json:"type"`
	Text string           `json:"text"`
}

type ContentBlockStopData struct {
	Type  EventType `json:"type"`
	Index int       `json:"index"`
}

type MessageDeltaData struct {
	Type  EventType        `json:"type"`
	Delta MessageDeltaInfo `json:"delta"`
	Usage UsageEnd         `json:"usage"`
}

type MessageDeltaInfo struct {
	StopReason   StopReason `json:"stop_reason"`
	StopSequence *string    `json:"stop_sequence"`
}

type UsageEnd struct {
	OutputTokens int `json:"output_tokens"`
}

type MessageStopData struct {
	Type EventType `json:"type"`
}

type ToolUseBlock struct {
	Type  ContentBlockType `json:"type"`
	ID    string           `json:"id"`
	Name  string           `json:"name"`
	Input any              `json:"input"`
}

type ContentBlockStartToolUseData struct {
	Type         EventType    `json:"type"`
	Index        int          `json:"index"`
	ContentBlock ToolUseBlock `json:"content_block"`
}

type ToolResultBlock struct {
	Type      ContentBlockType `json:"type"`
	ToolUseID string           `json:"tool_use_id"`
	Content   string           `json:"content,omitempty"`
	IsError   bool             `json:"is_error,omitempty"`
}

type ContentBlockStartToolResultData struct {
	Type         EventType       `json:"type"`
	Index        int             `json:"index"`
	ContentBlock ToolResultBlock `json:"content_block"`
}

type InputJSONDelta struct {
	Type        ContentBlockType `json:"type"`
	PartialJSON string           `json:"partial_json"`
}

type ContentBlockDeltaToolInputData struct {
	Type  EventType      `json:"type"`
	Index int            `json:"index"`
	Delta InputJSONDelta `json:"delta"`
}

type ToolResultDelta struct {
	Type ContentBlockType `json:"type"`
	Text string           `json:"text"`
}

type ContentBlockDeltaToolResultData struct {
	Type  EventType       `json:"type"`
	Index int             `json:"index"`
	Delta ToolResultDelta `json:"delta"`
}

// TimelineItemKind marks whether a timeline entry is text or a tool execution.
type TimelineItemKind = string

const (
	TimelineKindText TimelineItemKind = "text"
	TimelineKindTool TimelineItemKind = "tool"
)

// TimelineItem is one ordered entry produced by reassembly: a text segment OR
// a completed tool execution (call + result pair).
type TimelineItem struct {
	Kind      TimelineItemKind `json:"kind"`
	Text      string           `json:"text,omitempty"`
	Execution *ToolExecution   `json:"execution,omitempty"`
}

// ToolCall is one reassembled tool_use block.
type ToolCall struct {
	Name      string          `json:"name"`
	Params    json.RawMessage `json:"params"`
	ToolUseID string          `json:"tool_use_id"`
}

// ToolExecution is a matched tool_use + tool_result pair.
type ToolExecution struct {
	Name      string          `json:"name"`
	Params    json.RawMessage `json:"params"`
	Result    string          `json:"result"`
	ToolUseID string          `json:"tool_use_id"`
}
