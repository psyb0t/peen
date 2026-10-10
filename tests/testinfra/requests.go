package testinfra

import (
	"encoding/json"
	"strconv"
)

const (
	scriptedCallIDPrefix = "call-scripted-call-"
	jsonNullLiteral      = "null"
)

// ProviderMessage is one message of a completion request the provider mock
// received. Content is the message text as sent, or the raw JSON when the
// content is not a plain string.
type ProviderMessage struct {
	Role       string
	Content    string
	ToolCallID string
}

// ScriptedToolCall is one tool call a ScriptedToolTurn issues in Calls mode.
// ArgumentsFrom, when set, builds the arguments from the content of the tool
// results the turn already holds, oldest first, and wins over Arguments.
type ScriptedToolCall struct {
	Name          string
	Arguments     map[string]any
	ArgumentsFrom func(results []string) map[string]any
}

// ScriptedCallID is the tool call ID the provider mock gives the call at
// index in a ScriptedToolTurn's Calls.
func ScriptedCallID(index int) string {
	return scriptedCallIDPrefix + strconv.Itoa(index)
}

// ProviderRequests returns every completion request the provider mock has
// received, oldest first, each as its full message list. A test notes the
// length before it acts and reads the requests after that index.
func (i *Infra) ProviderRequests() [][]ProviderMessage {
	if i == nil || i.provider == nil {
		return nil
	}

	return i.provider.recordedRequests()
}

func (m *openAIModelsMock) recordRequest(request providerCompletionRequest) {
	messages := make([]ProviderMessage, 0, len(request.Messages))

	for _, message := range request.Messages {
		messages = append(messages, ProviderMessage{
			Role:       message.Role,
			Content:    message.contentText(),
			ToolCallID: message.ToolCallID,
		})
	}

	m.requestsMu.Lock()
	defer m.requestsMu.Unlock()

	m.requests = append(m.requests, messages)
}

func (m *openAIModelsMock) recordedRequests() [][]ProviderMessage {
	m.requestsMu.Lock()
	defer m.requestsMu.Unlock()

	return append([][]ProviderMessage(nil), m.requests...)
}

func (m providerCompletionMessage) contentText() string {
	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		return text
	}

	raw := string(m.Content)
	if raw == jsonNullLiteral {
		return ""
	}

	return raw
}
