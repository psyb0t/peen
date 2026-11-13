package agent

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

type fileRecoveryActor struct {
	sessionID  uuid.UUID
	agentRunID uuid.UUID
}

type fileRecoveryCache struct {
	recovery fileRecovery
	pending  map[string]bool
	offset   int
	used     uint64
}

func (r *Runtime) fileRecoveryObserver(
	ctx context.Context,
	observer func(uuid.UUID, Event),
) func(uuid.UUID, Event) {
	return func(sessionID uuid.UUID, event Event) {
		if err := r.retireFileRecovery(sessionID, event); err != nil {
			r.clearFileRecovery(sessionID, nil)
			ctxscope.GetLogger(ctx).Warn("retire file recovery actor",
				"err", err)
		}

		if observer != nil {
			observer(sessionID, event)
		}
	}
}

func (r *Runtime) retireFileRecovery(sessionID uuid.UUID, event Event) error {
	switch event.Type {
	case EventTypeTurnFailed, EventTypeTurnCancelled:
		// These outcomes can mark previously observed rows incomplete.
		r.clearFileRecovery(sessionID, nil)
	case EventTypeAgentRunCompleted, EventTypeAgentRunFailed,
		EventTypeAgentRunCancelled:
		var payload agentRunLivePayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return ctxerrors.Wrap(err, "decode finished file recovery actor")
		}

		if payload.AgentRunID == uuid.Nil {
			r.clearFileRecovery(sessionID, nil)

			return nil
		}

		r.clearFileRecovery(sessionID, &payload.AgentRunID)
	}

	return nil
}

func (r *Runtime) clearFileRecovery(
	sessionID uuid.UUID,
	agentRunID *uuid.UUID,
) {
	r.fileRecoveryMutex.Lock()
	defer r.fileRecoveryMutex.Unlock()

	for key := range r.fileRecoveryActors {
		if key.sessionID == sessionID &&
			(agentRunID == nil || key.agentRunID == *agentRunID) {
			delete(r.fileRecoveryActors, key)
		}
	}
}

// The lock covers paging as well as publication, so concurrent requests cannot
// observe a partially advanced cursor or mutate another request's inventory.
func (r *Runtime) cachedFileRecovery(
	ctx context.Context,
	conversation workflowConversation,
	input workflowRoundInput,
) (*fileRecovery, error) {
	r.fileRecoveryMutex.Lock()
	defer r.fileRecoveryMutex.Unlock()

	key := fileRecoveryActor{sessionID: input.Execution.SessionID}
	if conversation.Child != nil {
		key.agentRunID = conversation.Child.AgentRunID
	}

	if r.fileRecoveryActors == nil {
		r.fileRecoveryActors = make(map[fileRecoveryActor]*fileRecoveryCache)
	}

	cache := r.fileRecoveryActors[key]
	if cache == nil {
		cache = &fileRecoveryCache{
			recovery: fileRecovery{readCalls: make(map[string]bool)},
			pending:  make(map[string]bool),
		}
	}

	next := *cache
	next.recovery.reads = slices.Clone(cache.recovery.reads)
	next.recovery.readCalls = maps.Clone(cache.recovery.readCalls)
	next.pending = maps.Clone(cache.pending)

	if err := r.advanceFileRecovery(ctx, conversation, input,
		&next); err != nil {
		return nil, err
	}

	r.fileRecoveryClock++
	next.used = r.fileRecoveryClock

	if r.fileRecoveryActors[key] == nil {
		r.evictFileRecoveryActor()
	}

	r.fileRecoveryActors[key] = &next
	recovery := next.recovery
	recovery.reads = slices.Clone(recovery.reads)
	recovery.readCalls = maps.Clone(recovery.readCalls)

	return &recovery, nil
}

func (r *Runtime) evictFileRecoveryActor() {
	if len(r.fileRecoveryActors) < fileRecoveryMaxActors {
		return
	}

	var oldest fileRecoveryActor

	var used uint64
	for key, cache := range r.fileRecoveryActors {
		if used == 0 || cache.used < used {
			oldest, used = key, cache.used
		}
	}

	delete(r.fileRecoveryActors, oldest)
}

func (r *Runtime) advanceFileRecovery(
	ctx context.Context,
	conversation workflowConversation,
	input workflowRoundInput,
	cache *fileRecoveryCache,
) error {
	for {
		messages, more, err := r.fileRecoveryPage(ctx, conversation, input,
			cache.offset, fileRecoveryPageSize)
		if err != nil {
			return err
		}

		if len(messages) == 0 && cache.offset > 0 {
			// Pages have no total. Probe only when the tail is empty, to
			// distinguish an unchanged transcript from one that shrank.
			boundary, _, err := r.fileRecoveryPage(ctx, conversation, input,
				cache.offset-1, 1)
			if err != nil {
				return err
			}

			if len(boundary) == 0 {
				*cache = fileRecoveryCache{
					recovery: fileRecovery{readCalls: make(map[string]bool)},
					pending:  make(map[string]bool),
				}

				continue
			}
		}

		cache.recovery.observeMessages(messages, cache.pending)
		cache.offset += len(messages)

		if !more {
			return nil
		}
	}
}

// Read the raw, actor-local transcript, including rows before a summary.
// Only range identities survive each page; file contents are not cached.
func (r *Runtime) workflowFileRecovery(
	ctx context.Context,
	conversation workflowConversation,
	input workflowRoundInput,
) (*fileRecovery, error) {
	recovery, err := r.cachedFileRecovery(ctx, conversation, input)
	if err != nil {
		return nil, err
	}

	workspace := fileRecoveryWorkspace(conversation)

	if len(recovery.reads) == 0 || workspace == "" {
		return recovery, nil
	}

	executor, err := tools.NewExecutor(tools.Options{
		Workspace:  workspace,
		Filesystem: r.filesystem, Virtual: r.virtualFilesystem,
		Limits: r.toolLimits,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create file recovery reader")
	}

	recovery.read = executor.ReadFile

	return recovery, nil
}

func fileRecoveryWorkspace(conversation workflowConversation) string {
	if conversation.Runtime != nil &&
		conversation.Runtime.Directory.Directory != "" {
		return conversation.Runtime.Directory.Directory
	}

	if conversation.Child != nil {
		return conversation.Child.Workspace
	}

	if conversation.Root != nil {
		return conversation.Root.Workspace
	}

	return ""
}

func (r *fileRecovery) observeMessages(
	messages []elelem.Message,
	calls map[string]bool,
) {
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			calls[call.ID] = call.Name == toolNameReadFile
			r.readCalls[call.ID] = call.Name == toolNameReadFile
		}

		if message.Role != elelem.RoleTool {
			continue
		}

		if calls[message.ToolCallID] && !message.ToolResultIsError {
			r.observe(message.Text())
		}

		delete(calls, message.ToolCallID)
	}
}

func (r *Runtime) fileRecoveryPage(
	ctx context.Context,
	conversation workflowConversation,
	input workflowRoundInput,
	offset, limit int,
) ([]elelem.Message, bool, error) {
	if conversation.Child != nil {
		page, err := r.store.ListAgentRunMessages(ctx,
			input.Execution.SessionID, conversation.Child.AgentRunID,
			session.ListAgentRunMessagesOptions{
				Limit: limit, Offset: offset,
			})
		if err != nil {
			return nil, false, ctxerrors.Wrap(err, "read child file inventory")
		}

		var messages []elelem.Message

		for _, row := range page.Items {
			message, err := fileInventoryMessage(row.Role, row.Content,
				row.ToolCallsJSON, row.ToolCallID,
				row.IsError || row.Incomplete)
			if err != nil {
				return nil, false, err
			}

			messages = append(messages, message)
		}

		return messages, page.HasMore, nil
	}

	page, err := r.store.ListMessages(ctx, input.Execution.SessionID,
		session.ListMessagesOptions{
			Limit:  limit,
			Offset: offset, Order: session.PageOrderAscending,
		})
	if err != nil {
		return nil, false, ctxerrors.Wrap(err, "read session file inventory")
	}

	var messages []elelem.Message

	for _, row := range page.Items {
		message, err := fileInventoryMessage(row.Role, row.Content,
			row.ToolCallsJSON, row.ToolCallID, row.IsError || row.Incomplete)
		if err != nil {
			return nil, false, err
		}

		messages = append(messages, message)
	}

	return messages, page.HasMore, nil
}

func fileInventoryMessage(
	role models.MessageRole,
	content, callsJSON, callID string,
	isError bool,
) (elelem.Message, error) {
	converted, err := elelemRole(role)
	if err != nil {
		return elelem.Message{}, err
	}

	var calls []elelem.ToolCall
	if callsJSON != "" {
		if err := json.Unmarshal([]byte(callsJSON), &calls); err != nil {
			return elelem.Message{}, ctxerrors.Wrap(err,
				"decode file inventory calls")
		}
	}

	return elelem.Message{
		Role: converted, Content: elelem.Text(content),
		ToolCalls: calls, ToolCallID: callID,
		ToolResultIsError: isError,
	}, nil
}
