package session

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type executionFinishDecision struct {
	InputHash string `json:"inputHash"`
	Continued bool   `json:"continued"`
}

type executionFinishResolution struct {
	FinishDecision *executionFinishDecision `json:"finishDecision,omitempty"`
}

func finishDecisionHash(input CompleteActivityInput) (string, error) {
	if input.Finish == nil {
		return "", ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"finish decision input")
	}

	finish := *input.Finish
	pending := string(finish.PendingOutput)
	finish.PendingOutput = nil

	encoded, err := json.Marshal(struct {
		Output        string               `json:"output"`
		Context       string               `json:"context"`
		PendingOutput string               `json:"pendingOutput"`
		Finish        FinishExecutionInput `json:"finish"`
	}{string(input.Output), string(input.Context), pending, finish})
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode finish decision input")
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}

func repeatFinishDecision(
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
) (bool, error) {
	var resolution executionFinishResolution
	if err := json.Unmarshal([]byte(activity.ResolutionJSON),
		&resolution); err != nil {
		return false, ctxerrors.Wrap(err, "decode saved finish decision")
	}

	if resolution.FinishDecision == nil {
		return false, nil
	}

	if err := validateFinishDecisionOutput(activity, input,
		resolution.FinishDecision); err != nil {
		return false, ctxerrors.Wrap(err, "validate saved finish output")
	}

	digest, err := finishDecisionHash(input)
	if err != nil {
		return false, ctxerrors.Wrap(err, "hash repeated finish decision")
	}

	if subtle.ConstantTimeCompare([]byte(digest),
		[]byte(resolution.FinishDecision.InputHash)) != 1 {
		return false, ctxerrors.Wrap(commerr.ErrConflict,
			"finish decision input changed")
	}

	return resolution.FinishDecision.Continued, nil
}

func validateFinishDecisionOutput(
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
	decision *executionFinishDecision,
) error {
	if input.Finish == nil {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"saved finish decision requires original result")
	}

	expected := input.Output
	if decision.Continued {
		expected = input.Finish.PendingOutput
	}

	if activity.OutputJSON == nil ||
		*activity.OutputJSON != string(expected) ||
		activity.ContextJSON != string(input.Context) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"saved finish continuation differs")
	}

	return nil
}

// The continuation output and its request identity commit with the step event.
func (s *Store) savePendingFinishDecision(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
) error {
	continued := input
	continued.Output = input.Finish.PendingOutput
	continued.Finish = nil

	if err := s.saveActivityResult(ctx, query, activity,
		continued); err != nil {
		return ctxerrors.Wrap(err, "save pending finish output")
	}

	return s.saveFinishDecision(ctx, query, activity, input, true)
}

func (s *Store) saveFinishDecision(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
	continued bool,
) error {
	digest, err := finishDecisionHash(input)
	if err != nil {
		return ctxerrors.Wrap(err, "hash pending finish input")
	}

	resolution, err := json.Marshal(executionFinishResolution{
		FinishDecision: &executionFinishDecision{
			InputHash: digest, Continued: continued,
		},
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode pending finish decision")
	}

	repository := query.ExecutionActivity

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(activity.SessionID),
		repository.ID.Eq(activity.ID),
	).UpdateSimple(repository.ResolutionJSON.Value(string(resolution)))
	if err != nil {
		return ctxerrors.Wrap(err, "save pending finish request identity")
	}

	activity.ResolutionJSON = string(resolution)

	return nil
}

func (s *Store) finishActivityCompletion(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input CompleteActivityInput,
	events *[]EventInput,
) error {
	err := s.finishExecution(ctx, query, execution, input, events)
	if err == nil {
		if input.Finish != nil && (len(input.Finish.PendingOutput) != 0 ||
			input.Finish.Turn != nil || input.Finish.Child != nil) {
			return s.saveFinishDecision(ctx, query, activity, input, false)
		}

		return nil
	}

	if !errors.Is(err, ErrExecutionInboxPending) || input.Finish == nil ||
		len(input.Finish.PendingOutput) == 0 {
		return ctxerrors.Wrap(err, "finish activity execution")
	}

	return s.savePendingFinishDecision(ctx, query, activity, input)
}
