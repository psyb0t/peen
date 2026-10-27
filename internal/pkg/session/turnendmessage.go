package session

import (
	"context"
	"strings"
	"text/template"

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
	Guidance    string
}

// renderTurnEndMessage writes the system message for a turn that ended as
// ended describes.
func renderTurnEndMessage(ended TurnEndNotice) (string, error) {
	data := turnEndMessageData{
		Outcome:     turnEndFailedOutcome,
		Error:       ended.Error,
		Undelivered: ended.UndeliveredMessages,
		Guidance:    turnEndUnfinishedGuidance,
	}

	switch ended.State {
	case models.TurnStateCancelled:
		data.Outcome = turnEndCancelledOutcome
		data.Guidance = turnEndCancelledGuidance
	case models.TurnStateInterrupted:
		data.Outcome = turnEndInterruptedOutcome
	case models.TurnStateFailed,
		models.TurnStateRunning,
		models.TurnStateCompleted:
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

// appendTurnEndMessage adds the system message to the ended turn, after its
// other messages, in the transaction that ends it.
func (s *Store) appendTurnEndMessage(
	ctx context.Context,
	tx *repositories.Query,
	ended TurnEndNotice,
) error {
	text, err := renderTurnEndMessage(ended)
	if err != nil {
		return err
	}

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
