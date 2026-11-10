package agent

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/essessey"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// openToolCall is a tool call that started and has no result yet.
type openToolCall struct {
	callID    string
	name      string
	startedAt time.Time
}

// openToolCalls returns the turn's started calls that have no result, oldest
// first.
func (t *runtimeTurn) openToolCalls() []openToolCall {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	calls := make([]openToolCall, 0, len(t.toolStart))
	for callID, startedAt := range t.toolStart {
		calls = append(calls, openToolCall{
			callID: callID, name: t.toolNames[callID], startedAt: startedAt,
		})
	}

	slices.SortFunc(calls, func(left, right openToolCall) int {
		if order := left.startedAt.Compare(right.startedAt); order != 0 {
			return order
		}

		return strings.Compare(left.callID, right.callID)
	})

	return calls
}

// nextBlockIndex is the first content block index no recorded block uses.
func (t *runtimeTurn) nextBlockIndex() int {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	next := 0

	for _, event := range t.events {
		if !isWorkflowBlockEvent(event.Type) {
			continue
		}

		index, err := workflowStreamBlockIndex(event.Payload)
		if err != nil {
			continue
		}

		next = max(next, index+1)
	}

	return next
}

// recordStoppedToolCalls closes every tool call the turn started and never
// finished, so a stopped turn does not leave a tool.use without a result. Each
// call gets the same records a finished call gets: a tool message, a
// tool.result event, and a tool_result content block, marked as an error with
// session.StoppedToolResultContent. It writes after the turn's context ended,
// which is why it does not use ctx's cancellation.
func (p *preparedTurn) recordStoppedToolCalls(ctx context.Context) error {
	open := p.turn.openToolCalls()
	if len(open) == 0 {
		return nil
	}

	persistCtx := context.WithoutCancel(ctx)
	publisher := essessey.NewPublisher(persistCtx,
		transcriptSink{turn: p.turn})
	block := p.turn.nextBlockIndex()
	result := elelem.NewToolErrorResult(session.StoppedToolResultContent)

	for position, call := range open {
		event := elelem.ToolCallEvent{
			CallID: call.callID, Name: call.name, Index: position,
			Result: &result,
		}
		if err := p.recordToolResult(persistCtx, event,
			p.workspace); err != nil {
			return ctxerrors.Wrap(err, "record stopped tool result")
		}

		if err := publisher.SendToolResultBlock(block+position, call.callID,
			result.Content, result.IsError); err != nil {
			return ctxerrors.Wrap(err, "stream stopped tool result")
		}
	}

	return nil
}

// markToolStarted remembers a child's call until its result is stored.
func (s *agentRunSink) markToolStarted(call elelem.ToolCallEvent) {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()

	s.openTools = append(s.openTools, openToolCall{
		callID: call.CallID, name: call.Name, startedAt: time.Now(),
	})
}

// markToolFinished forgets a child's call once its result is stored.
func (s *agentRunSink) markToolFinished(callID string) {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()

	s.openTools = slices.DeleteFunc(s.openTools, func(call openToolCall) bool {
		return call.callID == callID
	})
}

// recordStoppedToolCalls closes every tool call the child started and never
// finished with a tool message and an agent.run.tool.result event marked as an
// error with session.StoppedToolResultContent, so the child's events do not
// show a tool.use without a result before agent.run.cancelled.
func (s *agentRunSink) recordStoppedToolCalls(ctx context.Context) error {
	s.toolMu.Lock()
	open := slices.Clone(s.openTools)
	s.toolMu.Unlock()

	result := elelem.NewToolErrorResult(session.StoppedToolResultContent)
	persistCtx := context.WithoutCancel(ctx)

	for position, call := range open {
		event := elelem.ToolCallEvent{
			CallID: call.callID, Name: call.name, Index: position,
			Result: &result,
		}
		if err := s.persistToolResult(persistCtx, event, "", ""); err != nil {
			return ctxerrors.Wrap(err, "record stopped child tool result")
		}
	}

	return nil
}
