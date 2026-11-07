package testinfra

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"

	"github.com/psyb0t/ctxerrors"
)

// ScriptedToolTurn drives readFile, one mutation, runCommand, and a final
// answer. UserMessage anchors the sequence in a durable transcript so old
// tool results from an earlier test turn cannot change its first round. Set
// exactly one of EditFileArguments and ApplyPatchArguments.
//
// Calls, when set, replaces that fixed sequence: the turn issues these tool
// calls in order, then answers with FinalAnswer. A request whose transcript
// lacks UserMessage, such as a child agent's, gets the default text
// completion instead.
//
// Children script child agent conversations the same way. A child script's
// UserMessage is the task text, which the child receives inside a <message>
// block naming its sender.
type ScriptedToolTurn struct {
	UserMessage         string
	Calls               []ScriptedToolCall
	ReadFileArguments   map[string]any
	EditFileArguments   map[string]any
	ApplyPatchArguments map[string]any
	RunCommandArguments map[string]any
	FinalAnswer         string
	Children            []ScriptedToolTurn
}

// CompletionHold exposes the point where a provider completion has reached the
// mock and a release operation for tests that need an active agent turn.
type CompletionHold struct {
	Observed  <-chan struct{}
	Cancelled <-chan struct{}
	release   func()
}

// Release allows the held provider completion to emit its deterministic stream.
func (h *CompletionHold) Release() {
	if h == nil || h.release == nil {
		return
	}

	h.release()
}

type openAIModelsMock struct {
	server          *httptest.Server
	modelsListed    atomic.Bool
	completionCount atomic.Int64
	holdMu          sync.Mutex
	nextHold        *completionHold
	requestsMu      sync.Mutex
	requests        [][]ProviderMessage
	scriptMu        sync.RWMutex
	script          *ScriptedToolTurn
	// promptMu guards both values recorded from the latest completion request.
	promptMu            sync.RWMutex
	lastSystemPrompt    string
	lastReasoningEffort string
}

// ProviderMock is a deterministic local OpenAI-compatible provider for
// process-form tests. It exposes no credentials and accepts only Peen's fixed
// integration model.
type ProviderMock struct {
	mock *openAIModelsMock
}

// NewProviderMock starts a local deterministic provider fixture.
func NewProviderMock() *ProviderMock {
	return &ProviderMock{mock: newOpenAIModelsMock()}
}

// BaseURL returns the fixture's OpenAI-compatible endpoint.
func (m *ProviderMock) BaseURL() string {
	if m == nil || m.mock == nil || m.mock.server == nil {
		return ""
	}

	return m.mock.server.URL
}

// DefaultModel returns the qualified model reference accepted by the fixture.
func (m *ProviderMock) DefaultModel() string {
	return providerModelReference
}

// EnableScriptedToolTurn makes later completions issue the supplied tool
// sequence before returning its final answer.
func (m *ProviderMock) EnableScriptedToolTurn(script ScriptedToolTurn) {
	if m == nil || m.mock == nil {
		return
	}

	m.mock.setScript(&script)
}

// DisableScriptedToolTurn restores the fixture's ordinary text response.
func (m *ProviderMock) DisableScriptedToolTurn() {
	if m == nil || m.mock == nil {
		return
	}

	m.mock.setScript(nil)
}

// LastSystemPrompt returns the latest completion request's system prompt.
func (m *ProviderMock) LastSystemPrompt() string {
	if m == nil || m.mock == nil {
		return ""
	}

	return m.mock.systemPrompt()
}

// Close stops the fixture server.
func (m *ProviderMock) Close() {
	if m == nil || m.mock == nil || m.mock.server == nil {
		return
	}

	m.mock.server.Close()
}

func (m *openAIModelsMock) setScript(script *ScriptedToolTurn) {
	m.scriptMu.Lock()
	defer m.scriptMu.Unlock()

	m.script = script
}

func (m *openAIModelsMock) currentScript() *ScriptedToolTurn {
	m.scriptMu.RLock()
	defer m.scriptMu.RUnlock()

	return m.script
}

type completionHold struct {
	observed    chan struct{}
	cancelled   chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

type providerCompletionRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
	//nolint:tagliatelle // The OpenAI chat completions API defines this name.
	ReasoningEffort string                      `json:"reasoning_effort"`
	Messages        []providerCompletionMessage `json:"messages"`
}

// providerCompletionMessage carries only the field a scripted turn needs:
// whether this transcript entry already carries a tool result.
type providerCompletionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	//nolint:tagliatelle // The OpenAI chat completions API defines this name.
	ToolCallID string `json:"tool_call_id"`
}

// scriptedToolMessageCount returns the number of tool results after the
// scripted request. Empty userMessage preserves whole-transcript behavior
// for callers that do not need an anchor.
func (r providerCompletionRequest) scriptedToolMessageCount(
	userMessage string,
) (int, error) {
	start := 0

	if userMessage != "" {
		for index, message := range r.Messages {
			if message.Role != providerRoleUser {
				continue
			}

			content := ""
			if err := json.Unmarshal(message.Content, &content); err != nil {
				return 0, ctxerrors.Wrap(err, "decode provider user message")
			}

			if content == userMessage {
				start = index + 1
			}
		}
	}

	count := 0

	for _, message := range r.Messages[start:] {
		if message.Role == providerRoleTool {
			count++
		}
	}

	return count, nil
}

func (r providerCompletionRequest) systemPrompt() string {
	for _, message := range r.Messages {
		if message.Role != providerRoleSystem {
			continue
		}

		var content string
		if err := json.Unmarshal(message.Content, &content); err == nil {
			return content
		}
	}

	return ""
}

func newOpenAIModelsMock() *openAIModelsMock {
	mock := &openAIModelsMock{}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))

	return mock
}

func (m *openAIModelsMock) handle(
	writer http.ResponseWriter,
	request *http.Request,
) {
	switch request.URL.Path {
	case providerModelsPath, providerModelsPathV1:
		m.handleModels(writer)
	case providerCompletionsPath, providerCompletionsPathV1:
		m.handleCompletion(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (m *openAIModelsMock) handleModels(writer http.ResponseWriter) {
	m.modelsListed.Store(true)
	writer.Header().Set("Content-Type", jsonMediaType)

	if err := json.NewEncoder(writer).Encode(map[string]any{
		providerObjectField: providerModelsObjectType,
		"data": []map[string]any{{
			"id":                providerModel,
			providerObjectField: providerModelObjectType,
		}},
	}); err != nil {
		http.Error(
			writer,
			"failed to encode mock model response",
			http.StatusInternalServerError,
		)
	}
}

func (m *openAIModelsMock) handleCompletion(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.Method != http.MethodPost {
		http.Error(
			writer,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)

		return
	}

	completionRequest, err := decodeCompletionRequest(request)
	if err != nil {
		http.Error(
			writer,
			"invalid completion request",
			http.StatusBadRequest,
		)

		return
	}

	if completionRequest.Model != providerModel || !completionRequest.Stream {
		http.Error(
			writer,
			"unexpected completion request",
			http.StatusBadRequest,
		)

		return
	}

	m.recordSystemPrompt(completionRequest.systemPrompt())
	m.recordReasoningEffort(completionRequest.ReasoningEffort)
	m.recordRequest(completionRequest)

	if !m.awaitCompletionRelease(request.Context()) {
		return
	}

	script := m.currentScript()
	if script == nil {
		m.writeCompletion(writer)

		return
	}

	m.writeScripted(writer, completionRequest, script)
}

// writeScripted answers a request under an active script: the issued Calls
// when the script lists them, the fixed tool sequence otherwise.
func (m *openAIModelsMock) writeScripted(
	writer http.ResponseWriter,
	request providerCompletionRequest,
	script *ScriptedToolTurn,
) {
	if len(script.Calls) > 0 {
		m.writeScriptedCalls(writer, request, script)

		return
	}

	round, err := request.scriptedToolMessageCount(script.UserMessage)
	if err != nil {
		http.Error(
			writer,
			"invalid scripted completion history",
			http.StatusBadRequest,
		)

		return
	}

	m.writeScriptedCompletion(writer, script, round)
}

func (m *openAIModelsMock) recordSystemPrompt(prompt string) {
	m.promptMu.Lock()
	defer m.promptMu.Unlock()

	m.lastSystemPrompt = prompt
}

func (m *openAIModelsMock) systemPrompt() string {
	m.promptMu.RLock()
	defer m.promptMu.RUnlock()

	return m.lastSystemPrompt
}

func (m *openAIModelsMock) recordReasoningEffort(effort string) {
	m.promptMu.Lock()
	defer m.promptMu.Unlock()

	m.lastReasoningEffort = effort
}

func (m *openAIModelsMock) reasoningEffort() string {
	m.promptMu.RLock()
	defer m.promptMu.RUnlock()

	return m.lastReasoningEffort
}

func (m *openAIModelsMock) writeScriptedCompletion(
	writer http.ResponseWriter,
	script *ScriptedToolTurn,
	round int,
) {
	stream, err := scriptedCompletionStream(round, script)
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

func decodeCompletionRequest(
	request *http.Request,
) (providerCompletionRequest, error) {
	completionRequest := providerCompletionRequest{}

	decoder := json.NewDecoder(request.Body)

	if err := decoder.Decode(&completionRequest); err != nil {
		return providerCompletionRequest{}, ctxerrors.Wrap(
			err,
			"decode completion request",
		)
	}

	return completionRequest, nil
}

func (m *openAIModelsMock) awaitCompletionRelease(ctx context.Context) bool {
	hold := m.takeNextCompletionHold()
	if hold == nil {
		return true
	}

	close(hold.observed)

	select {
	case <-ctx.Done():
		close(hold.cancelled)

		return false
	case <-hold.release:
		return true
	}
}

func (m *openAIModelsMock) writeCompletion(writer http.ResponseWriter) {
	stream, err := openAICompletionStream(m.completionCount.Add(1))
	if err != nil {
		http.Error(
			writer,
			"failed to encode mock completion response",
			http.StatusInternalServerError,
		)

		return
	}

	writer.Header().Set("Content-Type", eventStreamMediaType)

	if _, err := writer.Write(stream); err != nil {
		return
	}
}

func (m *openAIModelsMock) HoldNextCompletion() (*CompletionHold, error) {
	m.holdMu.Lock()
	defer m.holdMu.Unlock()

	if m.nextHold != nil {
		return nil, ctxerrors.New("a provider completion is already held")
	}

	hold := &completionHold{
		observed:  make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
	}
	m.nextHold = hold

	return &CompletionHold{
		Observed:  hold.observed,
		Cancelled: hold.cancelled,
		release: func() {
			hold.releaseOnce.Do(func() {
				close(hold.release)
			})
		},
	}, nil
}

func (m *openAIModelsMock) takeNextCompletionHold() *completionHold {
	m.holdMu.Lock()
	defer m.holdMu.Unlock()

	hold := m.nextHold
	m.nextHold = nil

	return hold
}
