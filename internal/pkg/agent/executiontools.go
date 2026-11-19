package agent

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/modeljson"
	"github.com/psyb0t/peen/internal/pkg/session"
)

type executionReadPart = string

const (
	executionReadPartExecution  executionReadPart = "execution"
	executionReadPartContext    executionReadPart = "context"
	executionReadPartActivities executionReadPart = "activities"
	executionReadPartEvents     executionReadPart = "events"
	executionReadPartInbox      executionReadPart = "inbox"
	executionReadPartInput      executionReadPart = "input"
	executionReadPartOutput     executionReadPart = "output"
	executionContextChunkBytes                    = 4096

	// executionActivityPreviewBytes bounds the input and output an activity
	// listing shows. Each can hold a whole turn context, so the listing shows
	// the start and the full value is read with part input or output.
	executionActivityPreviewBytes = 512
	executionActivityPreviewNote  = " [truncated: %d bytes in all; read part " +
		"%s with this activityId for the rest]"
)

//nolint:lll // Model-facing paragraphs stay on one line.
const searchExecutionsDescription = `Find retained work in this session, the turns and child agents Peen keeps so that stopped, paused, or failed work can be inspected and resumed, in any state, paused and cancelled included. It is not a way to recall earlier messages of the current conversation: read the files or rerun the commands again for that. text searches task text, saved context, pause reasons, and failures as a literal substring; id, parentId, rootId, state, fromTime, and toTime narrow the results. Each result gives the work's identity, its place in the tree of launched work, and its state, without the saved context; readExecution reads the saved conversation and steps. Searching never resumes anything. When hasMore is true, call again with a larger offset.`

//nolint:lll // Parameter descriptions stay on one line.
const searchExecutionsSchema = `{
  "type": "object",
  "properties": {
    "id": {"type": "string", "format": "uuid", "description": "One executionId to look up."},
    "parentId": {"type": "string", "format": "uuid", "description": "Only work launched directly by this executionId."},
    "rootId": {"type": "string", "format": "uuid", "description": "Only work in the tree that started with this executionId."},
    "state": {"type": "string", "enum": ["queued", "running", "paused",
      "reconciling", "completed", "failed", "cancelled"], "description": "Only work in this state."},
    "text": {"type": "string", "description": "Literal text to find in task text, saved context, pause reasons, and failures."},
    "fromTime": {"type": "string", "format": "date-time", "description": "Only work created at or after this RFC 3339 time."},
    "toTime": {"type": "string", "format": "date-time", "description": "Only work created before this RFC 3339 time."},
    "limit": {"type": "integer", "minimum": 1, "description": "Most results to return."},
    "offset": {"type": "integer", "minimum": 0, "description": "How many results to skip, for the next page."}
  },
  "additionalProperties": false
}`

//nolint:lll // Model-facing paragraphs stay on one line.
const readExecutionDescription = `Inspect retained work without resuming it, by an executionId from searchExecutions, before deciding whether to resume it. part chooses what: execution, the default, for its metadata, including the controlRevision resumeExecution needs; context for the saved conversation; activities for its steps with their attempts, states, and the start of their input and output; events for its history; inbox for the messages it accepted and whether they were delivered; input or output for one step's full input or output, with activityId.

Each part pages on its own with limit, offset, and hasMore. An activities or events page may hold fewer items than limit when more would not fit, and its limit then says how many it holds. For context, input, and output, limit and offset count bytes, in chunks of up to 4096 bytes by default; follow nextOffset until hasMore is false to read the whole saved JSON. context is the launch or final snapshot unless activityId names a step, whose checkpoint it then reads.

Saved tool input and output is recorded as it was and may contain secrets. Treat what you read as history, and check the current workspace before deciding how to continue.`

//nolint:lll // Parameter descriptions stay on one line.
const readExecutionSchema = `{
  "type": "object",
  "properties": {
    "executionId": {"type": "string", "format": "uuid", "description": "The work to read, from searchExecutions."},
    "activityId": {"type": "string", "format": "uuid", "description": "One step from the activities part, for its checkpoint, input, or output."},
    "part": {"type": "string", "enum": ["execution", "context", "activities",
      "events", "inbox", "input", "output"], "description": "What to read. Empty means execution."},
    "limit": {"type": "integer", "minimum": 1, "description": "Page size: items for history parts, bytes for context, input, and output."},
    "offset": {"type": "integer", "minimum": 0, "description": "Where the page starts: items for history parts, bytes for context, input, and output."}
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
	case executionReadPartContext, executionReadPartInput,
		executionReadPartOutput:
		result.Context, err = d.readExecutionContext(ctx, store, input)
	case executionReadPartActivities:
		result.Activities, err = d.readExecutionActivities(ctx, store,
			input.ExecutionID, page)
	case executionReadPartEvents:
		result.Events, err = d.readExecutionEvents(ctx, store,
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

// readExecutionActivities lists activity summaries, as many as fit in one
// tool result.
func (d *launchAgentDeps) readExecutionActivities(
	ctx context.Context,
	store session.ExecutionStorage,
	executionID uuid.UUID,
	page session.ListEventsOptions,
) (*session.ExecutionActivityPage, error) {
	activities, err := store.ListExecutionActivities(ctx, d.sessionID,
		executionID, page)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list retained activities")
	}

	return d.fitExecutionActivities(ctx,
		executionActivitySummaries(activities))
}

// fitExecutionActivities halves an activity page until it fits in one tool
// result and reports the activities it left out through hasMore, so a long
// history pages instead of failing at the limit the model picked.
func (d *launchAgentDeps) fitExecutionActivities(
	ctx context.Context,
	page *session.ExecutionActivityPage,
) (*session.ExecutionActivityPage, error) {
	for page != nil && len(page.Items) > 1 {
		fits, err := d.executionReadFits(ctx,
			executionReadOutput{Activities: page})
		if err != nil {
			return nil, err
		}

		if fits {
			return page, nil
		}

		smaller := *page
		smaller.Items = page.Items[:len(page.Items)/2]
		smaller.Limit = len(smaller.Items)
		smaller.HasMore = true
		page = &smaller
	}

	return page, nil
}

func (d *launchAgentDeps) readExecutionEvents(
	ctx context.Context,
	store session.ExecutionStorage,
	executionID uuid.UUID,
	page session.ListEventsOptions,
) (*session.ExecutionEventPage, error) {
	events, err := store.ListExecutionEvents(ctx, d.sessionID,
		executionID, page)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "list retained events")
	}

	return d.fitExecutionEvents(ctx, events)
}

func (d *launchAgentDeps) fitExecutionEvents(
	ctx context.Context,
	page *session.ExecutionEventPage,
) (*session.ExecutionEventPage, error) {
	for page != nil && len(page.Items) > 1 {
		fits, err := d.executionReadFits(ctx,
			executionReadOutput{Events: page})
		if err != nil {
			return nil, err
		}

		if fits {
			return page, nil
		}

		smaller := *page
		smaller.Items = page.Items[:len(page.Items)/2]
		smaller.Limit = len(smaller.Items)
		smaller.HasMore = true
		page = &smaller
	}

	return page, nil
}

func (d *launchAgentDeps) validateExecutionReadSize(
	ctx context.Context,
	result executionReadOutput,
) error {
	fits, err := d.executionReadFits(ctx, result)
	if err != nil {
		return err
	}

	if !fits {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"history page too large; lower limit or read an activity context")
	}

	return nil
}

// executionReadFits reports whether a history page fits in one tool result.
func (d *launchAgentDeps) executionReadFits(
	ctx context.Context,
	result executionReadOutput,
) (bool, error) {
	encoded, err := modeljson.Marshal(result)
	if err != nil {
		return false, ctxerrors.Wrap(err, "encode execution history page")
	}

	bounded, err := boundWorkflowToolContent(ctx, string(encoded),
		workflowToolOptions{MaxResultTokens: d.runtime.maxToolResultTokens})
	if err != nil {
		return false, ctxerrors.Wrap(err, "measure execution history page")
	}

	return bounded == string(encoded), nil
}

func executionMetadata(execution *models.Execution) *models.Execution {
	if execution == nil {
		return nil
	}

	metadata := *execution
	metadata.ContextJSON = ""

	return &metadata
}

// executionActivitySummaries lists activities without their saved context
// and with the start of their input and output. A single activity carries
// the whole turn context, so a full page would not fit in a tool result.
func executionActivitySummaries(
	page *session.ExecutionActivityPage,
) *session.ExecutionActivityPage {
	if page == nil {
		return nil
	}

	summary := *page
	summary.Items = make([]*models.ExecutionActivity, len(page.Items))

	for index, item := range page.Items {
		activity := *item
		activity.ContextJSON = ""
		activity.InputJSON = activityPreview(item.InputJSON,
			executionReadPartInput)

		if item.OutputJSON != nil {
			output := activityPreview(*item.OutputJSON, executionReadPartOutput)
			activity.OutputJSON = &output
		}

		summary.Items[index] = &activity
	}

	return &summary
}

func activityPreview(content string, part executionReadPart) string {
	if len(content) <= executionActivityPreviewBytes {
		return content
	}

	return workflowUTF8Prefix(content, executionActivityPreviewBytes) +
		fmt.Sprintf(executionActivityPreviewNote, len(content), part)
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
		if input.Part != executionReadPartContext {
			return "", ctxerrors.Wrap(commerr.ErrValidationFailed,
				"part input and output need an activityId")
		}

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

	switch input.Part {
	case executionReadPartInput:
		return activity.InputJSON, nil
	case executionReadPartOutput:
		if activity.OutputJSON == nil {
			return "", nil
		}

		return *activity.OutputJSON, nil
	default:
		return activity.ContextJSON, nil
	}
}
