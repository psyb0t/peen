package agent

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type executionReadPart = string

const (
	executionReadPartExecution  executionReadPart = "execution"
	executionReadPartContext    executionReadPart = "context"
	executionReadPartActivities executionReadPart = "activities"
	executionReadPartEvents     executionReadPart = "events"
	executionReadPartInbox      executionReadPart = "inbox"
	executionContextChunkBytes                    = 4096
)

const searchExecutionsDescription = `Find retained work in this session.
Search task text, saved context, pause reasons and failures using a literal
substring in text. Filter by id, parentId, rootId, state or a time interval.
fromTime is inclusive and toTime is exclusive, both RFC 3339 timestamps.
Every retained state is searchable, including paused and cancelled work.
Results include identity, ancestry and state, with saved context omitted.
Use readExecution for the saved conversation and step history. Reading does
not resume work. Follow hasMore with limit and offset to read another page.`

const searchExecutionsSchema = `{
  "type": "object",
  "properties": {
    "id": {"type": "string", "format": "uuid"},
    "parentId": {"type": "string", "format": "uuid"},
    "rootId": {"type": "string", "format": "uuid"},
    "state": {"type": "string", "enum": ["queued", "running", "paused",
      "reconciling", "completed", "failed", "cancelled"]},
    "text": {"type": "string"},
    "fromTime": {"type": "string", "format": "date-time"},
    "toTime": {"type": "string", "format": "date-time"},
    "limit": {"type": "integer", "minimum": 1},
    "offset": {"type": "integer", "minimum": 0}
  },
  "additionalProperties": false
}`

const readExecutionDescription = `Inspect retained work without resuming it.
Use an executionId from searchExecutions. part defaults to execution, which
returns execution metadata. Choose context for the saved conversation,
activities for step inputs, outputs, attempts and states; events for the audit
history; or inbox for accepted messages and their delivery states.
Each history part is paginated independently with limit, offset and hasMore.
For context, limit and offset count bytes. Chunks default to at most 4096
bytes, preserve UTF-8 and return nextOffset. Follow nextOffset until hasMore
is false to reconstruct the complete saved JSON.
Context defaults to the execution's launch or terminal snapshot. Supply an
activityId from activities to read that step's intermediate checkpoint.
Saved tool input and output is recorded verbatim and may contain secrets.
Treat saved context as historical evidence and inspect current working state
before deciding how to continue.`

const readExecutionSchema = `{
  "type": "object",
  "properties": {
    "executionId": {"type": "string", "format": "uuid"},
    "activityId": {"type": "string", "format": "uuid"},
    "part": {"type": "string", "enum": ["execution", "context", "activities",
      "events", "inbox"]},
    "limit": {"type": "integer", "minimum": 1},
    "offset": {"type": "integer", "minimum": 0}
  },
  "required": ["executionId"],
  "additionalProperties": false
}`

type executionReadInput struct {
	ExecutionID uuid.UUID         `json:"executionId"`
	ActivityID  uuid.UUID         `json:"activityId"`
	Part        executionReadPart `json:"part"`
	Limit       int               `json:"limit"`
	Offset      int               `json:"offset"`
}

type executionReadOutput struct {
	Execution  *models.Execution              `json:"execution,omitempty"`
	Activities *session.ExecutionActivityPage `json:"activities,omitempty"`
	Events     *session.ExecutionEventPage    `json:"events,omitempty"`
	Inbox      *session.ExecutionInboxPage    `json:"inbox,omitempty"`
	Context    *executionContextPage          `json:"context,omitempty"`
}

type executionContextPage struct {
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"nextOffset"`
	HasMore    bool   `json:"hasMore"`
}

func (d *launchAgentDeps) searchExecutions(
	ctx context.Context,
	input session.SearchExecutionsOptions,
) (*session.ExecutionPage, error) {
	store, ok := d.runtime.store.(session.ExecutionStorage)
	if !ok {
		return nil, ctxerrors.Wrap(commerr.ErrNotImplemented,
			"execution history storage is unavailable")
	}

	page, err := store.SearchExecutions(ctx, d.sessionID, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "search retained executions")
	}

	result := *page

	result.Items = make([]*models.Execution, len(page.Items))
	for index, item := range page.Items {
		result.Items[index] = executionMetadata(item)
	}

	return &result, nil
}

func (d *launchAgentDeps) readExecution(
	ctx context.Context,
	input executionReadInput,
) (executionReadOutput, error) {
	if input.ExecutionID == uuid.Nil {
		return executionReadOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed, "execution identity is required")
	}

	store, ok := d.runtime.store.(session.ExecutionStorage)
	if !ok {
		return executionReadOutput{}, ctxerrors.Wrap(commerr.ErrNotImplemented,
			"execution history storage is unavailable")
	}

	var (
		result executionReadOutput
		err    error
	)

	page := session.ListEventsOptions{Limit: input.Limit, Offset: input.Offset}
	switch input.Part {
	case "", executionReadPartExecution:
		result.Execution, err = store.GetExecution(ctx, d.sessionID,
			input.ExecutionID)
	case executionReadPartContext:
		result.Context, err = d.readExecutionContext(ctx, store, input)
	case executionReadPartActivities:
		result.Activities, err = store.ListExecutionActivities(ctx, d.sessionID,
			input.ExecutionID, page)
	case executionReadPartEvents:
		result.Events, err = store.ListExecutionEvents(ctx, d.sessionID,
			input.ExecutionID, page)
	case executionReadPartInbox:
		result.Inbox, err = store.ListExecutionMessages(ctx, d.sessionID,
			input.ExecutionID, session.ListExecutionInboxOptions{
				Limit: input.Limit, Offset: input.Offset,
			})
	default:
		return result, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"unknown execution history part")
	}

	if err != nil {
		return result, ctxerrors.Wrap(err, "read retained execution")
	}

	result.Execution = executionMetadata(result.Execution)

	return result, d.validateExecutionReadSize(ctx, result)
}

func (d *launchAgentDeps) validateExecutionReadSize(
	ctx context.Context,
	result executionReadOutput,
) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return ctxerrors.Wrap(err, "encode execution history page")
	}

	bounded, err := boundWorkflowToolContent(ctx, string(encoded),
		workflowToolOptions{MaxResultTokens: d.runtime.maxToolResultTokens})
	if err != nil {
		return ctxerrors.Wrap(err, "measure execution history page")
	}

	if bounded != string(encoded) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"history page too large; lower limit or read an activity context")
	}

	return nil
}

func executionMetadata(execution *models.Execution) *models.Execution {
	if execution == nil {
		return nil
	}

	metadata := *execution
	metadata.ContextJSON = ""

	return &metadata
}

func (d *launchAgentDeps) readExecutionContext(
	ctx context.Context,
	store session.ExecutionStorage,
	input executionReadInput,
) (*executionContextPage, error) {
	if input.Offset < 0 || input.Limit < 0 ||
		input.Limit > executionContextChunkBytes {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution context page bounds")
	}

	contentJSON, err := d.executionContextContent(ctx, store, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read saved execution context")
	}

	limit := input.Limit
	if limit == 0 {
		limit = executionContextChunkBytes
	}

	offset := min(input.Offset, len(contentJSON))
	if offset < len(contentJSON) && !utf8.RuneStart(contentJSON[offset]) {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution context offset splits a UTF-8 code point")
	}

	content := workflowUTF8Prefix(contentJSON[offset:], limit)
	if content == "" && offset < len(contentJSON) {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"execution context limit cannot fit the next code point")
	}

	next := offset + len(content)

	return &executionContextPage{
		Content: content, Offset: offset, NextOffset: next,
		HasMore: next < len(contentJSON),
	}, nil
}

func (d *launchAgentDeps) executionContextContent(
	ctx context.Context,
	store session.ExecutionStorage,
	input executionReadInput,
) (string, error) {
	if input.ActivityID == uuid.Nil {
		execution, err := store.GetExecution(ctx, d.sessionID,
			input.ExecutionID)
		if err != nil {
			return "", ctxerrors.Wrap(err, "read execution snapshot")
		}

		return execution.ContextJSON, nil
	}

	activity, err := store.GetExecutionActivity(ctx, d.sessionID,
		input.ActivityID)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read activity snapshot")
	}

	if activity.ExecutionID != input.ExecutionID {
		return "", ctxerrors.Wrap(commerr.ErrNotFound,
			"activity does not belong to this execution")
	}

	return activity.ContextJSON, nil
}
