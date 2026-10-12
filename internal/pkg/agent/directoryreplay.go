package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type directoryResultKey struct {
	turnID uuid.UUID
	callID string
}

type directoryResult struct {
	directory string
	sequence  int64
}

// currentDirectory recovers only paired, successful changeDirectory results.
// It reads the full durable transcript, including compacted and failed turns.
func (r *Runtime) currentDirectory(
	ctx context.Context,
	sessionID uuid.UUID,
	workspace string,
) (string, error) {
	result, err := r.currentDirectoryRecord(ctx, sessionID, workspace)
	if err != nil {
		return "", ctxerrors.Wrap(err, "recover directory record")
	}

	return result.directory, nil
}

func (r *Runtime) currentDirectoryRecord(
	ctx context.Context,
	sessionID uuid.UUID,
	workspace string,
) (directoryResult, error) {
	pending := make(map[directoryResultKey]directoryResult)

	for offset := 0; ; offset += directoryReplayPageSize {
		options := session.ListMessagesOptions{
			Limit: directoryReplayPageSize, Offset: offset,
			Order: session.PageOrderDescending,
		}

		page, err := r.store.ListMessages(ctx, sessionID, options)
		if err != nil {
			return directoryResult{}, ctxerrors.Wrap(err, "recover directory")
		}

		for _, message := range page.Items {
			if result, found := pairedDirectory(message, pending); found {
				return result, nil
			}
		}

		if !page.HasMore {
			return directoryResult{directory: r.toolWorkspace(workspace)}, nil
		}
	}
}

func replayDirectory(
	message *models.Message,
	pending map[directoryResultKey]directoryResult,
) (string, bool) {
	result, found := pairedDirectory(message, pending)

	return result.directory, found
}

func pairedDirectory(
	message *models.Message,
	pending map[directoryResultKey]directoryResult,
) (directoryResult, bool) {
	if message.Role == models.MessageRoleTool && !message.IsError {
		if message.Workspace != "" {
			key := directoryResultKey{
				turnID: message.TurnID, callID: message.ToolCallID,
			}
			pending[key] = directoryResult{
				directory: message.Workspace, sequence: message.Sequence,
			}
		}

		return directoryResult{}, false
	}

	if message.Role != models.MessageRoleAssistant {
		return directoryResult{}, false
	}

	var calls []elelem.ToolCall

	data := []byte(message.ToolCallsJSON)
	if err := json.Unmarshal(data, &calls); err != nil {
		return directoryResult{}, false
	}

	latest := directoryResult{}

	for _, call := range calls {
		key := directoryResultKey{turnID: message.TurnID, callID: call.ID}
		result, found := pending[key]
		delete(pending, key)

		if found && call.Name == toolNameChangeDirectory &&
			result.sequence > latest.sequence {
			latest = result
		}
	}

	return latest, latest.directory != ""
}
