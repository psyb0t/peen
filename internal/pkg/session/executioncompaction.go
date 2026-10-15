package session

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
)

type executionCompactionResolution struct {
	CompactionHash string `json:"compactionHash"`
}

func compactionInputHash(input *CompactionInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", ctxerrors.Wrap(err, "encode compaction input")
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}

func validateCompactionRepeat(
	activity *models.ExecutionActivity,
	input *CompactionInput,
) error {
	var saved executionCompactionResolution
	if err := json.Unmarshal([]byte(activity.ResolutionJSON),
		&saved); err != nil {
		return ctxerrors.Wrap(err, "decode compaction identity")
	}

	if input == nil && saved.CompactionHash == "" {
		return nil
	}

	digest, err := compactionInputHash(input)
	if err != nil {
		return ctxerrors.Wrap(err, "hash repeated compaction")
	}

	if subtle.ConstantTimeCompare([]byte(digest),
		[]byte(saved.CompactionHash)) != 1 {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"compaction completion changed")
	}

	return nil
}

func (s *Store) saveActivityCompaction(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	execution *models.Execution,
	input CompleteActivityInput,
) error {
	if input.Compaction == nil {
		return nil
	}

	if execution.Kind == models.ExecutionKindChild {
		return s.saveChildActivityCompaction(ctx, query, activity, execution,
			input)
	}

	if activity.Kind != models.ActivityKindCompaction ||
		execution.Kind != models.ExecutionKindRoot || input.Finish != nil {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compaction requires a root compaction activity")
	}

	normalized, err := s.validateActivityCompaction(*input.Compaction,
		activity.ID)
	if err != nil {
		return ctxerrors.Wrap(err, "validate compaction completion")
	}

	ids, err := validateCompactionHistory(ctx, query, execution.SessionID,
		normalized)
	if err != nil {
		return ctxerrors.Wrap(err, "validate compacted history")
	}

	if err := createCompactionForMessages(ctx, query,
		s.compactionRecord(execution.SessionID, normalized),
		ids); err != nil {
		return ctxerrors.Wrap(err, "commit compaction summary")
	}

	return saveCompactionIdentity(ctx, query, activity, input.Compaction)
}

func (s *Store) validateActivityCompaction(
	input CompactionInput,
	activityID uuid.UUID,
) (CompactionInput, error) {
	if invalidCompactionIdentity(input, activityID) ||
		invalidCompactionMetadata(input) {
		return CompactionInput{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"invalid compaction completion")
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		return CompactionInput{}, ctxerrors.Wrap(err, "encode compaction size")
	}

	if len(encoded) > s.maxStoredMessageBytes {
		return CompactionInput{}, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"compaction exceeds stored message limit")
	}

	input.ID = activityID

	return normalizeCompactionInput(input)
}

func saveCompactionIdentity(
	ctx context.Context,
	query *repositories.Query,
	activity *models.ExecutionActivity,
	input *CompactionInput,
) error {
	digest, err := compactionInputHash(input)
	if err != nil {
		return ctxerrors.Wrap(err, "hash compaction completion")
	}

	encoded, err := json.Marshal(executionCompactionResolution{digest})
	if err != nil {
		return ctxerrors.Wrap(err, "encode compaction identity")
	}

	repository := query.ExecutionActivity

	_, err = repository.WithContext(ctx).Where(
		repository.ID.Eq(activity.ID),
		repository.SessionID.Eq(activity.SessionID),
	).UpdateSimple(repository.ResolutionJSON.Value(string(encoded)))
	if err != nil {
		return ctxerrors.Wrap(err, "save compaction identity")
	}

	activity.ResolutionJSON = string(encoded)

	return nil
}

func invalidCompactionIdentity(
	input CompactionInput,
	activityID uuid.UUID,
) bool {
	return (input.ID != uuid.Nil && input.ID != activityID) ||
		input.FromMessageID == uuid.Nil || input.ToMessageID == uuid.Nil
}

func invalidCompactionMetadata(input CompactionInput) bool {
	return strings.TrimSpace(input.Summary) == "" ||
		strings.TrimSpace(input.ModelID) == "" ||
		strings.TrimSpace(input.PromptHash) == "" ||
		input.SourceMessageCount < 1 || input.InputTokenCount < 0 ||
		input.SummaryTokenCount < 0
}
