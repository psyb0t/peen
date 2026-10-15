package session

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type executionProjectionResolution struct {
	ProjectionHash string `json:"projectionHash"`
}

func (s *Store) saveActivityProjection(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input CompleteActivityInput,
	events *[]EventInput,
) error {
	if err := s.saveRootActivityProjection(ctx, query, activity, execution,
		input, events); err != nil {
		return ctxerrors.Wrap(err, "save root activity projection")
	}

	return s.saveChildActivityProjection(ctx, query, activity, execution,
		input.ChildProjection)
}

func (s *Store) validateRootProjection(input CompleteActivityInput) error {
	projection := input.RootProjection
	if projection == nil {
		return nil
	}

	if input.Finish != nil || input.Compaction != nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"root projection cannot replace finalization or compaction")
	}

	if err := validateExecutionFinishHolder(&projection.Holder); err != nil {
		return ctxerrors.Wrap(err, "validate projection holder")
	}

	encoded, err := json.Marshal(projection)
	if err != nil {
		return ctxerrors.Wrap(err, "encode root projection")
	}

	if len(encoded) > s.maxStoredMessageBytes {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"root projection exceeds storage limit")
	}

	for _, event := range projection.Events {
		if event.ID == uuid.Nil {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"root projection event identity")
		}
	}

	return nil
}

func projectionInputHash(input any) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode projection identity")
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}

func validateProjectionRepeat(
	activity *models.ExecutionActivity,
	input CompleteActivityInput,
) error {
	var saved executionProjectionResolution
	if err := json.Unmarshal([]byte(activity.ResolutionJSON),
		&saved); err != nil {
		return ctxerrors.Wrap(err, "decode saved projection identity")
	}

	projection := activityProjectionInput(input)
	if projection == nil && saved.ProjectionHash == "" {
		return nil
	}

	digest, err := projectionInputHash(projection)
	if err != nil {
		return ctxerrors.Wrap(err, "hash repeated root projection")
	}

	if subtle.ConstantTimeCompare([]byte(digest),
		[]byte(saved.ProjectionHash)) != 1 {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"activity transcript projection changed")
	}

	return nil
}

// The caller holds activeMu until the activity and transcript commit together.
func (s *Store) saveRootActivityProjection(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input CompleteActivityInput,
	events *[]EventInput,
) error {
	projection := input.RootProjection
	if projection == nil {
		return nil
	}

	if execution.Kind != models.ExecutionKindRoot ||
		execution.State != models.ExecutionStateRunning {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"root projection requires a running root execution")
	}

	if err := checkExecutionFinishHolder(ctx, query, activity, execution,
		&FinishExecutionInput{RootHolder: &projection.Holder}); err != nil {
		return ctxerrors.Wrap(err, "fence root projection holder")
	}

	lease := Lease{
		ID:        projection.Holder.LeaseID,
		SessionID: execution.SessionID, TurnID: execution.RootTurnID,
	}
	if !s.hasLeaseLocked(lease) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"root projection turn lease changed")
	}

	if _, err := s.appendTranscript(ctx, query, lease, projection.Messages,
		projection.Events); err != nil {
		return ctxerrors.Wrap(err, "append activity transcript")
	}

	if err := saveProjectionIdentity(ctx, query, activity,
		projection); err != nil {
		return ctxerrors.Wrap(err, "save root projection identity")
	}

	*events = append(*events, projection.Events...)

	return nil
}

func saveProjectionIdentity(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	input any,
) error {
	digest, err := projectionInputHash(input)
	if err != nil {
		return ctxerrors.Wrap(err, "hash root projection")
	}

	encoded, err := json.Marshal(executionProjectionResolution{
		ProjectionHash: digest,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode saved projection identity")
	}

	repository := query.ExecutionActivity

	_, err = repository.WithContext(ctx).Where(
		repository.SessionID.Eq(activity.SessionID),
		repository.ID.Eq(activity.ID),
	).UpdateSimple(repository.ResolutionJSON.Value(string(encoded)))
	if err != nil {
		return ctxerrors.Wrap(err, "save projection request identity")
	}

	activity.ResolutionJSON = string(encoded)

	return nil
}

func activityProjectionInput(input CompleteActivityInput) any {
	if input.RootProjection != nil {
		return input.RootProjection
	}

	if input.ChildProjection != nil {
		return input.ChildProjection
	}

	return nil
}
