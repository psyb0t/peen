package session

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

// TurnModelSummary projects a turn's audited model work for controller callers.
type TurnModelSummary struct {
	Model            string
	Text             string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// GetTurnModelSummary reads only calls belonging to this session and turn.
// Failed provider attempts count because they may also consume tokens.
func (s *Store) GetTurnModelSummary(
	ctx context.Context,
	sessionID, turnID uuid.UUID,
) (TurnModelSummary, error) {
	repository := s.query.ModelRun

	runs, err := repository.WithContext(ctx).
		Where(repository.SessionID.Eq(sessionID), repository.TurnID.Eq(turnID)).
		Order(repository.StartedAt, repository.ID).
		Find()
	if err != nil {
		return TurnModelSummary{}, ctxerrors.Wrap(err, "read turn model runs")
	}

	var summary TurnModelSummary

	for _, run := range runs {
		if run.Stage == models.ModelRunStageTurn {
			summary.Model = run.ModelReference
			summary.Text = run.ResponseText
		}

		err := s.addModelCallUsage(ctx, sessionID, run.ID, &summary)
		if err != nil {
			return TurnModelSummary{}, err
		}
	}

	return summary, nil
}

func (s *Store) addModelCallUsage(
	ctx context.Context,
	sessionID, modelRunID uuid.UUID,
	summary *TurnModelSummary,
) error {
	repository := s.query.ModelCall

	calls, err := repository.WithContext(ctx).
		Where(repository.SessionID.Eq(sessionID),
			repository.ModelRunID.Eq(modelRunID)).Find()
	if err != nil {
		return ctxerrors.Wrap(err, "read turn model call usage")
	}

	for _, call := range calls {
		summary.PromptTokens += call.PromptTokens + call.WastedPromptTokens
		summary.CompletionTokens += call.CompletionTokens +
			call.WastedCompletionTokens
		summary.TotalTokens += call.TotalTokens + call.WastedTotalTokens
	}

	return nil
}
