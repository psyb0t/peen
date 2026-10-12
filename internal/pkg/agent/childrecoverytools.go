package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

//nolint:lll // One model-visible recovery notice.
const interruptedToolResult = "The worker stopped before this call's result was recorded. Its effects are unknown. Inspect the current state before deciding whether to retry."

type recoveredWaitState struct {
	startedAt   time.Time
	answer      *agentAnswerPayload
	hasQuestion bool
}

func (r *Runtime) recoverChildTool(
	child recoveredChild,
	call elelem.ToolCall,
) (elelem.Message, error) {
	content, isError, err := r.recoveredToolContent(child, call)
	if err != nil {
		return elelem.Message{}, ctxerrors.Wrap(
			err, "restore child tool outcome",
		)
	}

	if err := child.sink.onToolResult(child.ctx, elelem.ToolCallEvent{
		CallID: call.ID, Name: call.Name,
		Result: &elelem.ToolResult{Content: content, IsError: isError},
	}); err != nil {
		return elelem.Message{}, ctxerrors.Wrap(
			err, "checkpoint recovered tool",
		)
	}

	return elelem.Message{
		Role: elelem.RoleTool, Content: elelem.Text(content),
		ToolCallID: call.ID, ToolResultIsError: isError,
	}, nil
}

func (r *Runtime) recoveredToolContent(
	child recoveredChild,
	call elelem.ToolCall,
) (string, bool, error) {
	if call.Name == toolNameLaunchAgent {
		return r.recoveredLaunchResult(child, call)
	}

	if call.Name != toolNameWaitMessages && call.Name != toolNameAsk {
		return interruptedToolResult, true, nil
	}

	output, err := r.recoverChildWait(child, call)
	if err != nil {
		return "", false, ctxerrors.Wrap(err, "resume passive child wait")
	}

	encoded, err := json.Marshal(output)
	if err != nil {
		return "", false, ctxerrors.Wrap(err, "encode recovered wait result")
	}

	return string(encoded), false, nil
}

func (r *Runtime) recoverChildWait(
	child recoveredChild,
	call elelem.ToolCall,
) (any, error) {
	input, err := recoveredWaitInput(call)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "decode pending wait")
	}

	timeout, err := r.recoveredWaitTimeout(call.Name, input.Timeout)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending wait timeout")
	}

	events, err := r.recoveryEvents(child)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read pending wait checkpoints")
	}

	state, err := recoveredWaitCheckpoint(events, call)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore pending wait checkpoint")
	}

	if state.startedAt.IsZero() {
		state.startedAt = child.stored.StartedAt
	}

	waited, err := r.resumeChildWait(child, call, input, state, timeout)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resume pending wait")
	}

	if call.Name != toolNameAsk {
		return waited, nil
	}

	output := askOutput{
		Answered: !waited.TimedOut, TimedOut: waited.TimedOut,
	}
	if state.answer == nil {
		if err := child.sink.emit(
			child.ctx, EventTypeAgentRunAnswer,
			agentAnswerPayload{Answered: output.Answered, CallID: call.ID},
		); err != nil {
			return nil, ctxerrors.Wrap(err, "checkpoint recovered answer")
		}
	}

	return output, nil
}

func recoveredWaitInput(call elelem.ToolCall) (askInput, error) {
	var input askInput
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		return input, ctxerrors.Wrap(err, "decode recovered wait input")
	}

	if call.Name == toolNameAsk && strings.TrimSpace(input.Question) == "" {
		return input, ctxerrors.Wrap(
			commerr.ErrValidationFailed, "question is required",
		)
	}

	return input, nil
}

func (r *Runtime) recoveredWaitTimeout(
	name, input string,
) (time.Duration, error) {
	var (
		timeout time.Duration
		err     error
	)
	if name == toolNameAsk {
		timeout, err = r.askTimeoutFor(input)
	} else {
		timeout, err = optionalWaitTimeout(input)
	}

	if err != nil {
		return 0, ctxerrors.Wrap(err, "parse recovered wait timeout")
	}

	if timeout == 0 || timeout > messageToolTimeout {
		timeout = messageToolTimeout
	}

	return timeout, nil
}

func (r *Runtime) resumeChildWait(
	child recoveredChild,
	call elelem.ToolCall,
	input askInput,
	state recoveredWaitState,
	timeout time.Duration,
) (waitMessagesOutput, error) {
	if state.answer != nil {
		return waitMessagesOutput{TimedOut: !state.answer.Answered}, nil
	}

	if timeout > 0 {
		timeout -= r.now().Sub(state.startedAt)
		if timeout <= 0 {
			return waitMessagesOutput{TimedOut: true}, nil
		}
	}

	if call.Name == toolNameAsk && !state.hasQuestion {
		if err := child.sink.emit(
			child.ctx, EventTypeAgentRunQuestion,
			agentQuestionPayload{Question: input.Question, CallID: call.ID},
		); err != nil {
			return waitMessagesOutput{}, ctxerrors.Wrap(
				err, "restore unsent question",
			)
		}

		if err := child.deps.sendQuestion(
			child.ctx, child.run, input.Question,
		); err != nil {
			return waitMessagesOutput{}, ctxerrors.Wrap(
				err, "send recovered question",
			)
		}
	}

	waited, err := waitForMessages(
		child.ctx, timeout, child.run.inbox.arrival, nil, noNotices,
	)
	if err != nil {
		return waitMessagesOutput{}, ctxerrors.Wrap(
			err, "wait for recovered inbox",
		)
	}

	return waited, nil
}

func recoveredWaitCheckpoint(
	events []*models.AgentRunEvent,
	call elelem.ToolCall,
) (recoveredWaitState, error) {
	state := recoveredWaitState{}

	for _, event := range events {
		switch event.EventType {
		case EventTypeAgentRunToolUse:
			var payload toolUsePayload
			if err := json.Unmarshal(
				[]byte(event.PayloadJSON), &payload,
			); err != nil {
				return state, ctxerrors.Wrap(
					err, "decode recovered tool checkpoint",
				)
			}

			if payload.CallID == call.ID {
				state = recoveredWaitState{startedAt: event.CreatedAt}
			}
		case EventTypeAgentRunQuestion:
			if err := state.restoreQuestion(event, call); err != nil {
				return state, ctxerrors.Wrap(err, "restore question checkpoint")
			}
		case EventTypeAgentRunAnswer:
			if err := state.restoreAnswer(event, call); err != nil {
				return state, ctxerrors.Wrap(err, "restore answer checkpoint")
			}
		}
	}

	return state, nil
}

func (s *recoveredWaitState) restoreQuestion(
	event *models.AgentRunEvent,
	call elelem.ToolCall,
) error {
	var payload agentQuestionPayload
	if err := json.Unmarshal(
		[]byte(event.PayloadJSON), &payload,
	); err != nil {
		return ctxerrors.Wrap(err, "decode recovered question checkpoint")
	}

	if !s.startedAt.IsZero() && matchesRecoveredCall(payload.CallID, call.ID) {
		s.hasQuestion = true
	}

	return nil
}

func (s *recoveredWaitState) restoreAnswer(
	event *models.AgentRunEvent,
	call elelem.ToolCall,
) error {
	if !isRecoveredAnswer(call, *s) {
		return nil
	}

	var payload agentAnswerPayload
	if err := json.Unmarshal(
		[]byte(event.PayloadJSON), &payload,
	); err != nil {
		return ctxerrors.Wrap(err, "decode recovered answer checkpoint")
	}

	if matchesRecoveredCall(payload.CallID, call.ID) {
		s.answer = &payload
	}

	return nil
}

func matchesRecoveredCall(storedID, callID string) bool {
	return storedID == "" || storedID == callID
}

func isRecoveredAnswer(call elelem.ToolCall, state recoveredWaitState) bool {
	return call.Name == toolNameAsk && !state.startedAt.IsZero()
}

func (r *Runtime) recoveredLaunchResult(
	child recoveredChild,
	call elelem.ToolCall,
) (string, bool, error) {
	for offset := 0; ; offset += session.MaximumPageLimit {
		page, err := r.store.ListAgentRuns(
			child.ctx, child.stored.SessionID, session.ListAgentRunsOptions{
				Limit: session.MaximumPageLimit, Offset: offset,
				ParentAgentRunID: &child.run.ID,
			},
		)
		if err != nil {
			return "", false, ctxerrors.Wrap(
				err, "find checkpointed child launch",
			)
		}

		for _, run := range page.Items {
			if run.ParentToolCallID != call.ID {
				continue
			}

			output := launchAgentOutput{
				AgentRunID: run.ID, Name: run.Name,
				Definition:       string(run.Definition),
				ParentAgentRunID: child.run.ID.String(),
			}

			encoded, err := json.Marshal(output)
			if err != nil {
				return "", false, ctxerrors.Wrap(
					err, "encode checkpointed launch",
				)
			}

			return string(encoded), false, nil
		}

		if !page.HasMore {
			return interruptedToolResult, true, nil
		}
	}
}
