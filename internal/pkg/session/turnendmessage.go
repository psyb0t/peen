package session

import (
	"context"
	"strings"
	"text/template"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

const (
	// turnEndMessageTemplateText is the system message Peen adds to a turn
	// that ended without completing. The turn's own messages come before it
	// in the next turn's history, so it says only what they cannot: how the
	// turn ended, which calls never returned, and what the agent should do
	// about it.
	turnEndMessageTemplateText = `This turn did not finish. {{.Outcome}}
{{- if .Error}}

Error: {{.Error}}
{{- end}}
{{- if .Unanswered}}

These tool calls never returned a result, so their effect is unknown:
{{- range .Unanswered}}
- {{.Name}} {{.Arguments}}
{{- end}}
{{- end}}
{{- range .Undelivered}}

A message the user sent during this turn that never reached you:
<message>
{{.}}
</message>
{{- end}}
{{- if .ExecutionID}}

{{if .RanNoTool}}Peen kept this turn as execution {{.ExecutionID}}, but ` +
		`it stopped before it ran any tool, so it changed nothing and there ` +
		`is nothing in it to inspect. If a later message asks you to ` +
		`continue it, do its work directly or continue it with ` +
		`resumeExecution.
{{- else}}Peen kept this turn's work as execution {{.ExecutionID}}. If a ` +
		`later message asks you to continue it, inspect it with ` +
		`readExecution and continue it with resumeExecution instead of ` +
		`redoing it.
{{- if .Unanswered}} Resolve each tool call above whose effect is unknown ` +
		`with resolveExecutionActivity first.
{{- end}}
{{- end}}
{{- end}}

{{.Guidance}}`

	turnEndMessageTemplateName = "turn-end-message"

	turnEndFailedOutcome      = "It failed."
	turnEndInterruptedOutcome = "It was interrupted when Peen stopped " +
		"before the turn ended."
	turnEndCancelledOutcome = "The user stopped it."

	// turnEndUnfinishedGuidance follows a turn that broke rather than one
	// the user stopped: what it was asked still stands.
	turnEndUnfinishedGuidance = "Its work may be half done. Check the " +
		"workspace before you repeat any of it. The user still wants what " +
		"this turn's message asked for, together with any later message, " +
		"unless a later message says otherwise."
	turnEndCancelledGuidance = "Its work may be half done. Check the " +
		"workspace before you repeat any of it. Do not continue this turn's " +
		"work unless a later message asks for it."

	// The kept guidance replaces the two above when Peen retained the turn's
	// work, so the agent continues that work rather than redoing it by hand.
	turnEndKeptUnfinishedGuidance = "The user still wants what this " +
		"turn's message asked for, together with any later message, unless a " +
		"later message says otherwise."
	turnEndKeptCancelledGuidance = "Do not continue this turn's work " +
		"unless a later message asks for it."

	// turnEndResumedLead goes in front of an end message when Peen resumes
	// the turn it ended, so later history does not claim the turn stopped
	// for good.
	turnEndResumedLead = "Peen later resumed this turn, and its work " +
		"continued after this message. What was true when it stopped:\n\n"
)

// turnEndMessageData is what turnEndMessageTemplateText renders.
type turnEndMessageData struct {
	Outcome     string
	Error       string
	Unanswered  []TurnEndToolCall
	Undelivered []string
	// ExecutionID names the execution that kept the turn's work. It is empty
	// when Peen kept none.
	ExecutionID string
	// RanNoTool reports that the kept execution started no tool, so there is
	// no work in it to inspect.
	RanNoTool bool
	Guidance  string
}

// turnEndOutcome picks how the message describes an ended turn and what it
// tells the agent to do next. isKept reports whether Peen kept the turn's
// work in an execution the agent can resume.
func turnEndOutcome(
	state models.TurnState,
	isKept bool,
) (string, string) {
	switch state {
	case models.TurnStateCancelled:
		if isKept {
			return turnEndCancelledOutcome, turnEndKeptCancelledGuidance
		}

		return turnEndCancelledOutcome, turnEndCancelledGuidance
	case models.TurnStateInterrupted:
		if isKept {
			return turnEndInterruptedOutcome, turnEndKeptUnfinishedGuidance
		}

		return turnEndInterruptedOutcome, turnEndUnfinishedGuidance
	case models.TurnStateFailed,
		models.TurnStateRunning,
		models.TurnStateCompleted:
	}

	if isKept {
		return turnEndFailedOutcome, turnEndKeptUnfinishedGuidance
	}

	return turnEndFailedOutcome, turnEndUnfinishedGuidance
}

// renderTurnEndMessage writes the system message for a turn that ended as
// ended describes. kept is the execution that kept the turn's work, with an
// ID of uuid.Nil when Peen kept none.
func renderTurnEndMessage(
	ended TurnEndNotice,
	kept keptExecution,
) (string, error) {
	executionID := kept.ID
	isKept := executionID != uuid.Nil
	outcome, guidance := turnEndOutcome(ended.State, isKept)

	data := turnEndMessageData{
		Outcome:     outcome,
		Error:       ended.Error,
		Undelivered: ended.UndeliveredMessages,
		RanNoTool:   isKept && !kept.HasToolActivity,
		Guidance:    guidance,
	}

	if isKept {
		data.ExecutionID = executionID.String()
	}

	for _, call := range ended.ToolCalls {
		if call.Outcome == turnEndToolNoResult {
			data.Unanswered = append(data.Unanswered, call)
		}
	}

	parsed, err := template.New(turnEndMessageTemplateName).
		Parse(turnEndMessageTemplateText)
	if err != nil {
		return "", ctxerrors.Wrap(err, "parse turn end message template")
	}

	var rendered strings.Builder
	if err := parsed.Execute(&rendered, data); err != nil {
		return "", ctxerrors.Wrap(err, "render turn end message")
	}

	return rendered.String(), nil
}

// markTurnEndMessagesResumed notes on a turn's end messages that Peen resumed
// the turn. A message already marked is left as it is.
func markTurnEndMessagesResumed(
	ctx context.Context,
	tx *repositories.Query,
	turn *models.Turn,
) error {
	repository := tx.Message

	ended, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(turn.SessionID),
		repository.TurnID.Eq(turn.ID),
		repository.Role.Eq(string(models.MessageRoleSystem)),
	).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read resumed turn end messages")
	}

	for _, message := range ended {
		if strings.HasPrefix(message.Content, turnEndResumedLead) {
			continue
		}

		if _, err := repository.WithContext(ctx).Where(
			repository.ID.Eq(message.ID),
		).UpdateSimple(repository.Content.Value(
			turnEndResumedLead + message.Content,
		)); err != nil {
			return ctxerrors.Wrap(err, "mark turn end message resumed")
		}
	}

	return nil
}

// keptExecution is the root execution that holds an ended turn's work.
type keptExecution struct {
	// ID is uuid.Nil when the turn has no execution or its execution is
	// over.
	ID uuid.UUID
	// HasToolActivity reports whether the execution started any of the
	// model's tool calls. An execution stopped during its first model call or
	// compaction has none, even when hooks already ran.
	HasToolActivity bool
}

// findKeptExecution returns the root execution that holds an ended turn's
// work. The execution exists whether Peen pauses it before or after the turn
// ends.
func findKeptExecution(
	ctx context.Context,
	tx *repositories.Query,
	turn *models.Turn,
) (keptExecution, error) {
	repository := tx.Execution

	roots, err := repository.WithContext(ctx).Where(
		repository.SessionID.Eq(turn.SessionID),
		repository.RootTurnID.Eq(turn.ID),
		repository.Kind.Eq(string(models.ExecutionKindRoot)),
	).Order(repository.CreatedAt).Find()
	if err != nil {
		return keptExecution{}, ctxerrors.Wrap(err,
			"read ended turn execution")
	}

	for _, root := range roots {
		if isExecutionTerminal(root.State) {
			continue
		}

		activities := tx.ExecutionActivity

		tools, err := activities.WithContext(ctx).Where(
			activities.SessionID.Eq(turn.SessionID),
			activities.ExecutionID.Eq(root.ID),
			activities.Kind.Eq(string(models.ActivityKindTool)),
			activities.Name.Eq(ExecutionToolPhaseActivityName),
		).Count()
		if err != nil {
			return keptExecution{}, ctxerrors.Wrap(err,
				"count kept execution tool activities")
		}

		return keptExecution{ID: root.ID, HasToolActivity: tools > 0}, nil
	}

	return keptExecution{}, nil
}

// appendTurnEndMessage adds the system message to the ended turn, after its
// other messages, in the transaction that ends it.
func (s *Store) appendTurnEndMessage(
	ctx context.Context,
	tx *repositories.Query,
	ended TurnEndNotice,
) error {
	turnRepository := tx.Turn

	turn, err := turnRepository.WithContext(ctx).Where(
		turnRepository.ID.Eq(ended.TurnID),
	).First()
	if err != nil {
		return ctxerrors.Wrap(err, "read ended turn")
	}

	session, err := s.findSessionWithQuery(ctx, tx, turn.SessionID)
	if err != nil {
		return ctxerrors.Wrap(err, "read ended turn session")
	}

	kept, err := findKeptExecution(ctx, tx, turn)
	if err != nil {
		return err
	}

	text, err := renderTurnEndMessage(ended, kept)
	if err != nil {
		return err
	}

	result, err := s.appendMessages(ctx, tx,
		Lease{SessionID: turn.SessionID, TurnID: turn.ID},
		session.MessageCount, turn.Workspace,
		[]MessageInput{{
			Role:     models.MessageRoleSystem,
			Content:  text,
			Injected: true,
		}},
	)
	if err != nil {
		return ctxerrors.Wrap(err, "append turn end message")
	}

	repository := tx.Session
	now := s.now()

	if _, err := repository.WithContext(ctx).
		Where(repository.ID.Eq(turn.SessionID)).
		UpdateSimple(
			repository.MessageCount.Add(result.messageCount),
			repository.LastMessageAt.Value(now),
			repository.UpdatedAt.Value(now),
		); err != nil {
		return ctxerrors.Wrap(err, "count turn end message")
	}

	return nil
}
