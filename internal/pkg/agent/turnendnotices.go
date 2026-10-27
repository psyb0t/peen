package agent

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// unansweredReplyText stands in for an assistant reply of an unfinished turn
// that has nothing left once its unanswered tool calls are left out. A
// provider refuses an empty assistant message.
const unansweredReplyText = "(This reply only made tool calls that never " +
	"returned.)"

// lateToolResultFormat replays a tool result stored apart from its call, such
// as one a turn received after it ended. It takes the tool name, the call ID,
// and the result.
const lateToolResultFormat = "A result for the earlier %s tool call %s " +
	"arrived after its turn stopped:\n%s"

// lateToolResultUnknownName names a call whose request is no longer in
// history, such as one a summary replaced.
const lateToolResultUnknownName = "unknown"

// pairToolResults returns history in the shape a provider accepts, where the
// results of an assistant message's tool calls follow it directly. A turn
// that did not finish can leave a call with no result, or with a result
// stored later, after the system message Peen added when the turn ended.
// Such a call is left out of its reply, and a result stored apart from its
// call becomes a system message naming the call. Every row keeps its
// identity, so a reconstruction plan still maps one message to each row.
func pairToolResults(history []*models.Message) ([]*models.Message, error) {
	paired := slices.Clone(history)
	names := make(map[string]string)

	for index := 0; index < len(paired); index++ {
		if paired[index] == nil {
			continue
		}

		switch paired[index].Role {
		case models.MessageRoleTool:
			paired[index] = lateToolResult(paired[index], names)
		case models.MessageRoleAssistant:
			last, err := pairAssistantCalls(paired, index, names)
			if err != nil {
				return nil, err
			}

			index = last
		case models.MessageRoleSystem, models.MessageRoleUser:
		}
	}

	return paired, nil
}

// pairAssistantCalls keeps the calls of the assistant message at index whose
// results follow it directly, and returns the index of its last result.
func pairAssistantCalls(
	paired []*models.Message,
	index int,
	names map[string]string,
) (int, error) {
	var calls []elelem.ToolCall
	if err := json.Unmarshal(
		[]byte(paired[index].ToolCallsJSON), &calls,
	); err != nil {
		return 0, ctxerrors.Wrap(err, "unmarshal stored tool calls")
	}

	pending := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		names[call.ID] = call.Name
		pending[call.ID] = struct{}{}
	}

	last := index
	for last+1 < len(paired) &&
		paired[last+1].Role == models.MessageRoleTool {
		if _, ok := pending[paired[last+1].ToolCallID]; !ok {
			break
		}

		delete(pending, paired[last+1].ToolCallID)
		last++
	}

	if len(pending) == 0 {
		return last, nil
	}

	reply, err := withoutUnansweredToolCalls(paired[index], calls, pending)
	if err != nil {
		return 0, err
	}

	paired[index] = reply

	return last, nil
}

// withoutUnansweredToolCalls returns a copy of message without the calls in
// unanswered. The system message Peen added when the turn ended names them.
func withoutUnansweredToolCalls(
	message *models.Message,
	calls []elelem.ToolCall,
	unanswered map[string]struct{},
) (*models.Message, error) {
	kept := make([]elelem.ToolCall, 0, len(calls))

	for _, call := range calls {
		if _, ok := unanswered[call.ID]; !ok {
			kept = append(kept, call)
		}
	}

	encoded, err := json.Marshal(kept)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "encode answered tool calls")
	}

	reply := *message
	reply.ToolCallsJSON = string(encoded)

	if len(kept) == 0 && reply.Content == "" {
		reply.Content = unansweredReplyText
	}

	return &reply, nil
}

// lateToolResult returns a copy of a tool result stored apart from its call
// as a system message that names the call.
func lateToolResult(
	message *models.Message,
	names map[string]string,
) *models.Message {
	name := names[message.ToolCallID]
	if name == "" {
		name = lateToolResultUnknownName
	}

	note := *message
	note.Role = models.MessageRoleSystem
	note.Content = fmt.Sprintf(lateToolResultFormat, name,
		message.ToolCallID, message.Content)
	note.ToolCallID = ""
	note.IsError = false

	return &note
}

// withoutTurnEndNotices returns batch without the notices Peen recorded when a
// turn ended unfinished. That turn's own messages and the system message Peen
// added to it reach the model in history, so the notice would only repeat
// them as untrusted event data. Clients still receive every notice.
func withoutTurnEndNotices(batch events.Batch) events.Batch {
	kept := events.Batch{Dropped: batch.Dropped}

	for _, notice := range batch.Notices {
		if isTurnEndNotice(notice) {
			continue
		}

		kept.Notices = append(kept.Notices, notice)
	}

	return kept
}

// isTurnEndNotice reports a notice Peen recorded when a turn ended without
// completing. The turn. prefix is reserved, so no outside caller can publish
// one.
func isTurnEndNotice(notice events.Notice) bool {
	return notice.Source == session.TurnEndNoticeSource &&
		(notice.Type == EventTypeTurnFailed ||
			notice.Type == EventTypeTurnCancelled)
}

// renderModelEvents renders the part of batch the model reads as an events
// block, or nothing when no notice is left for it.
func renderModelEvents(batch events.Batch) string {
	kept := withoutTurnEndNotices(batch)
	if len(kept.Notices) == 0 && kept.Dropped == 0 {
		return ""
	}

	return renderSessionEvents(kept)
}
