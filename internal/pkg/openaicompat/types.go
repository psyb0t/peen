package openaicompat

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"

	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
)

// Runner routes a request through Peen's controller-owned conversation path.
type Runner interface {
	Run(context.Context, control.ConversationRequest) (
		*control.ConversationResult, error,
	)
}

// Dependencies holds the controller's existing runtime and deployment limits.
type Dependencies struct {
	Runner               Runner
	Models               agent.ModelAPI
	DefaultModel         string
	DefaultWorkspace     string
	MaxRequestBytes      int64
	MaxImages            int
	MaxImageBytes        int
	MaxOutputSchemaBytes int
}

type handler struct {
	dependencies Dependencies
}

// OpenAI owns the snake_case names on this compatibility surface.
//
//nolint:tagliatelle // These JSON names are defined by OpenAI.
type completionRequest struct {
	Model           string          `json:"model"`
	Messages        []message       `json:"messages"`
	Stream          bool            `json:"stream"`
	ReasoningEffort string          `json:"reasoning_effort"`
	ResponseFormat  *responseFormat `json:"response_format"`
	Tools           json.RawMessage `json:"tools"`
	ToolChoice      json.RawMessage `json:"tool_choice"`
	Functions       json.RawMessage `json:"functions"`
	Logprobs        json.RawMessage `json:"logprobs"`
	N               *int            `json:"n"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

//nolint:tagliatelle // These JSON names are defined by OpenAI.
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text"`
	ImageURL *imageURL `json:"image_url"`
}

type imageURL struct {
	URL string `json:"url"`
}

//nolint:tagliatelle // These JSON names are defined by OpenAI.
type responseFormat struct {
	Type       string        `json:"type"`
	JSONSchema *schemaFormat `json:"json_schema"`
}

type schemaFormat struct {
	Schema json.RawMessage `json:"schema"`
}

type responseError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type errorEnvelope struct {
	Error responseError `json:"error"`
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

//nolint:tagliatelle // These JSON names are defined by OpenAI.
type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

//nolint:tagliatelle // These JSON names are defined by OpenAI.
type usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type completion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   usage              `json:"usage"`
}

//nolint:tagliatelle // These JSON names are defined by OpenAI.
type completionChoice struct {
	Index        int               `json:"index"`
	Message      completionMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type completionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

//nolint:tagliatelle // The x-peen extension belongs to this OpenAI surface.
type chunk struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	Created   int64          `json:"created"`
	Model     string         `json:"model"`
	Choices   []chunkChoice  `json:"choices"`
	Usage     *usage         `json:"usage,omitempty"`
	Extension *errorEnvelope `json:"x-peen,omitempty"`
}

//nolint:tagliatelle // These JSON names are defined by OpenAI.
type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role    string  `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}

type streamWriter struct {
	response  http.ResponseWriter
	id        string
	model     string
	created   int64
	started   bool
	wroteText bool
}

type streamEvent struct {
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type priorConversation struct {
	XMLName  xml.Name       `xml:"priorConversation"`
	Messages []priorMessage `xml:"message"`
}

type priorMessage struct {
	Role string `xml:"role,attr"`
	Body string `xml:",chardata"`
}
