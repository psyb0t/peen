package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	dabluveees "github.com/psyb0t/aichteeteapee/serbewr/dabluvee-es"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Local mirrors of the unexported root and agent.run event payload shapes
// that api/asyncapi.yaml describes. The real structs (agent.turnStartedPayload, textDeltaPayload,
// providerRetryPayload, turnCompletedPayload, turnFailedPayload,
// toolUsePayload, toolResultPayload, sessionEventsPayload,
// harnessWarningPayload, and the agent.run.* equivalents built in
// launchagent.go) are package-private to internal/pkg/agent, so this
// contract test, which lives in internal/pkg/http/server, rebuilds their
// wire shape with the same JSON tags instead of reaching into unexported
// code. Where the catalogue names a reachable, exported type backing a
// payload (events.Notice, harness.Warning, essessey's content-block types),
// the case below uses that real type rather than a hand-rolled one.
type (
	contractUserMessagePayload struct {
		Message string `json:"message"`
	}

	contractTurnStartedPayload struct {
		SessionID string `json:"sessionId"`
		Model     string `json:"model"`
		Workspace string `json:"workspace"`
	}

	contractSkillInjectedPayload struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}

	contractTextPayload struct {
		Text string `json:"text"`
	}

	contractProviderRetryPayload struct {
		Attempt int    `json:"attempt"`
		Reason  string `json:"reason"`
		Status  int    `json:"status"`
		DelayMS int64  `json:"delayMs"`
	}

	contractToolUsePayload struct {
		CallID    string         `json:"callId"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}

	contractToolResultPayload struct {
		CallID  string `json:"callId"`
		Name    string `json:"name"`
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	}

	contractSessionEventsPayload struct {
		Notices []events.Notice `json:"notices"`
		Dropped int             `json:"dropped"`
	}

	contractHarnessWarningPayload struct {
		Warnings []harness.Warning `json:"warnings"`
	}

	contractTurnCompletedPayload struct {
		Model string `json:"model"`
		Text  string `json:"text"`
	}

	contractTurnTerminalReasonPayload struct {
		Reason string `json:"reason"`
	}

	contractQuestionCallPayload struct {
		Question string  `json:"question"`
		CallID   *string `json:"callId,omitempty"`
	}

	contractAnswerCallPayload struct {
		Answered bool    `json:"answered"`
		CallID   *string `json:"callId,omitempty"`
	}

	contractAgentRunStartedPayload struct {
		AgentRunID   uuid.UUID `json:"agentRunId"`
		ParentTurnID uuid.UUID `json:"parentTurnId"`
		Name         string    `json:"name"`
		Definition   string    `json:"definition"`
		Depth        int       `json:"depth"`
		Model        string    `json:"model"`
		Workspace    string    `json:"workspace"`
	}

	contractAgentRunToolCall struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}

	contractPart struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}

	contractAgentRunAssistantMessagePayload struct {
		Role      string                     `json:"role"`
		Content   []contractPart             `json:"content"`
		ToolCalls []contractAgentRunToolCall `json:"toolCalls"`
	}

	contractAgentRunMessageInjectedPayload struct {
		Type    string `json:"type"`
		Content string `json:"content"`
		Phase   string `json:"phase"`
		Tool    string `json:"tool"`
		CallID  string `json:"callId"`
		Round   int    `json:"round"`
	}

	contractAgentRunInboxMessagePayload struct {
		Message string `json:"message"`
	}

	contractAgentRunTerminalPayload struct {
		AgentRunID            uuid.UUID `json:"agentRunId"`
		State                 string    `json:"state"`
		FailureClassification string    `json:"failureClassification,omitempty"`
		FailureDetail         string    `json:"failureDetail,omitempty"`
	}
)

// contractFrameCase is one server frame type the contract test renders
// through the real renderer and validates against wsapi.ServerEvent.
type contractFrameCase struct {
	name      string
	eventType string
	build     func(t *testing.T) *dabluveees.Event
}

// contractSkippedEventTypes lists agent.EventTypes() entries this test does
// not render. The catalogue (ws-frame-catalogue.md section 4.2) confirms the
// runtime never emits a root text.delta or thinking.delta, so no wsapi
// variant exists for either and no frame can be built to exercise one.
//
//nolint:gochecknoglobals // A fixed lookup table, never reassigned.
var contractSkippedEventTypes = map[string]string{
	agent.EventTypeTextDelta: "root text.delta is declared but never " +
		"emitted (ws-frame-catalogue.md section 4.2)",
	agent.EventTypeThinkingDelta: "root thinking.delta is declared but " +
		"never emitted (ws-frame-catalogue.md section 4.2)",
}

// TestEveryServerFrameTypeValidatesAgainstTheWsapiContract renders one frame
// of every server frame type Peen emits through the real renderer
// (feedItemFrame for feed-sourced frames, the completion/failure builders
// for the other two) and validates each against wsapi.ServerEvent.
//
// It also walks agent.EventTypes(), the list the agent and session packages
// emit from, so a type added there with no case here, and therefore no
// proof it has a matching wsapi schema variant, fails this test instead of
// surfacing as a silent contract gap in production.
func TestEveryServerFrameTypeValidatesAgainstTheWsapiContract(t *testing.T) {
	cases := contractFrameCases()
	covered := make(map[string]bool, len(cases))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := tc.build(t)
			require.Equal(t, tc.eventType, string(frame.Type))

			encoded, err := json.Marshal(frame)
			require.NoError(t, err)

			target := wsapi.ServerEvent{}
			require.NoErrorf(
				t,
				target.UnmarshalJSON(encoded),
				"frame type %q failed the wsapi contract: %s",
				tc.eventType,
				encoded,
			)
		})

		covered[tc.eventType] = true
	}

	for _, eventType := range agent.EventTypes() {
		if reason, skipped := contractSkippedEventTypes[eventType]; skipped {
			t.Logf("skipping %q: %s", eventType, reason)

			continue
		}

		assert.Truef(
			t,
			covered[eventType],
			"agent.EventTypes() has %q with no contract test case; give it "+
				"a wsapi schema variant and a case in contractFrameCases",
			eventType,
		)
	}
}

func contractFrameCases() []contractFrameCase {
	toolArguments := map[string]any{"path": "main.go"}

	cases := []contractFrameCase{
		contractRootCase(
			"turn.started",
			agent.EventTypeTurnStarted,
			contractTurnStartedPayload{
				SessionID: uuid.New().String(),
				Model:     "openai/gpt-5",
				Workspace: "/workspace",
			},
		),
		contractRootCase(
			"userMessage.created",
			agent.EventTypeUserMessageCreated,
			contractUserMessagePayload{Message: "hi"},
		),
		contractRootCase(
			"userMessage.queued",
			agent.EventTypeUserMessageQueued,
			contractUserMessagePayload{Message: "hi"},
		),
		contractRootCase(
			"userMessage.delivered",
			agent.EventTypeUserMessageDelivered,
			contractUserMessagePayload{Message: "hi"},
		),
		contractRootCase(
			"skill.injected",
			agent.EventTypeSkillInjected,
			contractSkillInjectedPayload{
				Name:    "core:planning",
				Content: "<skill>\n<name>core:planning</name>\n</skill>",
			},
		),
		contractRootCase(
			"provider.retry",
			agent.EventTypeProviderRetry,
			contractProviderRetryPayload{
				Attempt: 1,
				Reason:  "rate_limited",
				Status:  429,
				DelayMS: 500,
			},
		),
		contractRootCase(
			"tool.use",
			agent.EventTypeToolUse,
			contractToolUsePayload{
				CallID:    "call-1",
				Name:      "readFile",
				Arguments: toolArguments,
			},
		),
		contractRootCase(
			"tool.result",
			agent.EventTypeToolResult,
			contractToolResultPayload{
				CallID:  "call-1",
				Name:    "readFile",
				Content: "ok",
				IsError: false,
			},
		),
		contractRootCase(
			"session.events",
			agent.EventTypeSessionEvents,
			contractSessionEventsPayload{
				Notices: []events.Notice{{
					ID:        uuid.New(),
					SessionID: uuid.New(),
					Type:      "test.notice",
					Source:    "contract-test",
					Summary:   "a notice delivered to the model",
					Delivery:  events.DeliveryQueue,
					CreatedAt: time.Now().UTC(),
				}},
				Dropped: 0,
			},
		),
		contractRootCase(
			"harness.warning",
			agent.EventTypeHarnessWarning,
			contractHarnessWarningPayload{
				Warnings: []harness.Warning{{
					Kind:   harness.SourceKindSkill,
					Source: ".agents/skills/broken/SKILL.md",
					Reason: "missing name",
				}},
			},
		),
		contractRootCase(
			"turn.completed",
			agent.EventTypeTurnCompleted,
			contractTurnCompletedPayload{Model: "openai/gpt-5", Text: "done"},
		),
		contractRootCase(
			"turn.failed",
			agent.EventTypeTurnFailed,
			contractTurnTerminalReasonPayload{Reason: "agentRun"},
		),
		contractRootCase(
			"turn.cancelled",
			agent.EventTypeTurnCancelled,
			contractTurnTerminalReasonPayload{Reason: "cancelled"},
		),
		contractRootCase(
			"question.asked",
			agent.EventTypeQuestionAsked,
			contractQuestionCallPayload{
				Question: "proceed?",
				CallID:   new("call-1"),
			},
		),
		contractRootCase(
			"question.answered",
			agent.EventTypeQuestionAnswered,
			contractAnswerCallPayload{
				Answered: true,
				CallID:   new("call-1"),
			},
		),
		contractRootCase(
			"content_block_start",
			essessey.EventTypeContentBlockStart,
			essessey.ContentBlockStartToolUseData{
				Type:  essessey.EventTypeContentBlockStart,
				Index: 0,
				ContentBlock: essessey.ToolUseBlock{
					Type:  essessey.ContentBlockTypeToolUse,
					ID:    "call-1",
					Name:  "readFile",
					Input: map[string]any{},
				},
			},
		),
		contractRootCase(
			"content_block_delta",
			essessey.EventTypeContentBlockDelta,
			essessey.ContentBlockDeltaData{
				Type:  essessey.EventTypeContentBlockDelta,
				Index: 0,
				Delta: essessey.TextDelta{
					Type: essessey.ContentBlockTypeTextDelta,
					Text: "hi",
				},
			},
		),
		contractRootCase(
			"content_block_stop",
			essessey.EventTypeContentBlockStop,
			essessey.ContentBlockStopData{
				Type:  essessey.EventTypeContentBlockStop,
				Index: 0,
			},
		),

		contractAgentRunCase(
			"agent.run.started",
			agent.EventTypeAgentRunStarted,
			contractAgentRunStartedPayload{
				AgentRunID:   uuid.New(),
				ParentTurnID: uuid.New(),
				Name:         "reviewer",
				Definition:   "stored",
				Depth:        1,
				Model:        "openai/gpt-5",
				Workspace:    "/workspace",
			},
		),
		contractAgentRunCase(
			"agent.run.text.delta",
			agent.EventTypeAgentRunTextDelta,
			contractTextPayload{Text: "hi"},
		),
		contractAgentRunCase(
			"agent.run.thinking.delta",
			agent.EventTypeAgentRunThinkingDelta,
			contractTextPayload{Text: "hmm"},
		),
		contractAgentRunCase(
			"agent.run.tool.use",
			agent.EventTypeAgentRunToolUse,
			contractToolUsePayload{
				CallID:    "call-2",
				Name:      "readFile",
				Arguments: toolArguments,
			},
		),
		contractAgentRunCase(
			"agent.run.tool.result",
			agent.EventTypeAgentRunToolResult,
			contractToolResultPayload{
				CallID:  "call-2",
				Name:    "readFile",
				Content: "ok",
				IsError: false,
			},
		),
		contractAgentRunCase(
			"agent.run.assistant.message",
			agent.EventTypeAgentRunAssistantMessage,
			contractAgentRunAssistantMessagePayload{
				Role:    "assistant",
				Content: []contractPart{{Type: "text", Text: "done"}},
				ToolCalls: []contractAgentRunToolCall{{
					ID:        "call-2",
					Name:      "readFile",
					Arguments: toolArguments,
				}},
			},
		),
		contractAgentRunCase(
			"agent.run.message.injected",
			agent.EventTypeAgentRunMessageInjected,
			contractAgentRunMessageInjectedPayload{
				Type:    "user",
				Content: "<skill>\n<name>core:planning</name>\n</skill>",
				Phase:   "pre-tool",
				Tool:    "readFile",
				CallID:  "call-2",
				Round:   1,
			},
		),
		contractAgentRunCase(
			"agent.run.provider.retry",
			agent.EventTypeAgentRunProviderRetry,
			contractProviderRetryPayload{
				Attempt: 1,
				Reason:  "timeout",
				Status:  0,
				DelayMS: 250,
			},
		),
		contractAgentRunCase(
			"agent.run.message.queued",
			agent.EventTypeAgentRunMessageQueued,
			contractAgentRunInboxMessagePayload{Message: "hold on"},
		),
		contractAgentRunCase(
			"agent.run.message.delivered",
			agent.EventTypeAgentRunMessageDelivered,
			contractAgentRunInboxMessagePayload{Message: "hold on"},
		),
		contractAgentRunCase(
			"agent.run.message.undelivered",
			agent.EventTypeAgentRunMessageUndelivered,
			contractAgentRunInboxMessagePayload{Message: "hold on"},
		),
		contractAgentRunCase(
			"agent.run.question",
			agent.EventTypeAgentRunQuestion,
			contractQuestionCallPayload{
				Question: "ok?",
				CallID:   new("call-3"),
			},
		),
		contractAgentRunCase(
			"agent.run.answer",
			agent.EventTypeAgentRunAnswer,
			contractAnswerCallPayload{
				Answered: true,
				CallID:   new("call-3"),
			},
		),
		contractAgentRunCase(
			"agent.run.completed",
			agent.EventTypeAgentRunCompleted,
			contractAgentRunTerminalPayload{
				AgentRunID: uuid.New(),
				State:      "completed",
			},
		),
		contractAgentRunCase(
			"agent.run.failed",
			agent.EventTypeAgentRunFailed,
			contractAgentRunTerminalPayload{
				AgentRunID:            uuid.New(),
				State:                 "failed",
				FailureClassification: "agentRun",
				FailureDetail:         "boom",
			},
		),
		contractAgentRunCase(
			"agent.run.cancelled",
			agent.EventTypeAgentRunCancelled,
			contractAgentRunTerminalPayload{
				AgentRunID:            uuid.New(),
				State:                 "cancelled",
				FailureClassification: "cancelled",
			},
		),

		contractNoticeCase(),
		contractMessageCompletedCase(),
		contractMessageFailedCase(),
	}

	return cases
}

// contractRootCase builds a case for a root `events` row: the frame type and
// the data a FeedKindEvent item carries verbatim.
func contractRootCase(
	name string,
	eventType string,
	payload any,
) contractFrameCase {
	return contractFrameCase{
		name:      name,
		eventType: eventType,
		build: func(t *testing.T) *dabluveees.Event {
			t.Helper()

			encoded, err := json.Marshal(payload)
			require.NoError(t, err)

			item := session.FeedItem{
				SessionID:   uuid.New(),
				Sequence:    1,
				ID:          uuid.New(),
				Kind:        session.FeedKindEvent,
				EventType:   eventType,
				RequestID:   uuid.New(),
				PayloadJSON: string(encoded),
				CreatedAt:   time.Now().UTC(),
			}

			frame, err := feedItemFrame(item)
			require.NoError(t, err)

			return frame
		},
	}
}

// contractAgentRunCase builds a case for an agent.run.* feed row: the frame
// type and the inner "event" payload agentRunFeedFrameData wraps.
func contractAgentRunCase(
	name string,
	eventType string,
	event any,
) contractFrameCase {
	return contractFrameCase{
		name:      name,
		eventType: eventType,
		build: func(t *testing.T) *dabluveees.Event {
			t.Helper()

			encoded, err := json.Marshal(event)
			require.NoError(t, err)

			item := session.FeedItem{
				SessionID:    uuid.New(),
				Sequence:     1,
				ID:           uuid.New(),
				Kind:         session.FeedKindAgentRunEvent,
				EventType:    eventType,
				AgentRunID:   uuid.New(),
				ParentTurnID: uuid.New(),
				PayloadJSON:  string(encoded),
				CreatedAt:    time.Now().UTC(),
			}

			frame, err := feedItemFrame(item)
			require.NoError(t, err)

			return frame
		},
	}
}

// contractNoticeCase builds notice.created through the same
// agent.SessionNoticeModelToAPI path feedItemFrameData uses for a stored
// session notice.
func contractNoticeCase() contractFrameCase {
	return contractFrameCase{
		name:      "notice.created",
		eventType: session.FeedEventTypeNoticeCreated,
		build: func(t *testing.T) *dabluveees.Event {
			t.Helper()

			notice := &models.SessionNotice{
				ID:        uuid.New(),
				SessionID: uuid.New(),
				Type:      "test.contract.notice",
				Source:    "contract-test",
				Summary:   "a contract test notice",
				DataJSON:  `{"ok":true}`,
				Delivery:  models.NoticeDeliveryQueue,
				CreatedAt: time.Now().UTC().Truncate(time.Second),
			}

			item := session.FeedItem{
				SessionID: uuid.New(),
				Sequence:  1,
				ID:        notice.ID,
				Kind:      session.FeedKindNotice,
				EventType: session.FeedEventTypeNoticeCreated,
				Notice:    notice,
				CreatedAt: notice.CreatedAt,
			}

			frame, err := feedItemFrame(item)
			require.NoError(t, err)

			return frame
		},
	}
}

// contractMessageCompletedCase renders message.completed through the same
// newWebSocketEvent builder broadcastWebSocketMessageCompletion uses.
func contractMessageCompletedCase() contractFrameCase {
	return contractFrameCase{
		name:      "message.completed",
		eventType: webSocketMessageCompletedEventType,
		build: func(t *testing.T) *dabluveees.Event {
			t.Helper()

			return newWebSocketEvent(
				webSocketMessageCompletedEventType,
				webSocketMessageResult{Queued: false},
				uuid.New(),
				uuid.New(),
				uuid.New(),
			)
		},
	}
}

// contractMessageFailedCase renders message.failed through the same
// newWebSocketEvent and newWebSocketMessageFailure builders
// broadcastWebSocketFailure uses.
func contractMessageFailedCase() contractFrameCase {
	return contractFrameCase{
		name:      "message.failed",
		eventType: webSocketMessageFailedEventType,
		build: func(t *testing.T) *dabluveees.Event {
			t.Helper()

			return newWebSocketEvent(
				webSocketMessageFailedEventType,
				newWebSocketMessageFailure(
					ErrorCodeHarnessConfigurationInvalid,
					webSocketHarnessConfigurationMessage,
					webSocketInvalidSkillReason,
				),
				uuid.New(),
				uuid.New(),
				uuid.New(),
			)
		},
	}
}
