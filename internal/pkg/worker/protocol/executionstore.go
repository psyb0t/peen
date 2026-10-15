package protocol

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

var _ session.ExecutionStorage = (*Store)(nil)

//nolint:ireturn // The RPC decoder returns the requested concrete wire type.
func executionCall[R any](
	ctx context.Context,
	store *Store,
	sessionID uuid.UUID,
	request executionRequest,
) (R, error) {
	return call[R](ctx, store, methodExecutionStore,
		InputRequest[executionRequest]{SessionID: sessionID, Input: request})
}

func (s *Store) CreateExecution(
	ctx context.Context,
	sessionID uuid.UUID,
	input session.CreateExecutionInput,
) (*models.Execution, error) {
	return executionCall[*models.Execution](ctx, s, sessionID,
		executionRequest{Operation: executionCreate, Create: input})
}

func (s *Store) GetExecution(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) (*models.Execution, error) {
	return executionCall[*models.Execution](ctx, s, sessionID,
		executionRequest{Operation: executionGet, ExecutionID: executionID})
}

func (s *Store) SearchExecutions(
	ctx context.Context,
	sessionID uuid.UUID,
	options session.SearchExecutionsOptions,
) (*session.ExecutionPage, error) {
	return executionCall[*session.ExecutionPage](ctx, s, sessionID,
		executionRequest{Operation: executionSearch, Search: options})
}

func (s *Store) PauseExecution(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	reason string,
) ([]uuid.UUID, error) {
	return executionCall[[]uuid.UUID](ctx, s, sessionID,
		executionRequest{
			Operation: executionPause, ExecutionID: executionID, Reason: reason,
		})
}

func (s *Store) ResumeExecution(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
) ([]uuid.UUID, error) {
	return executionCall[[]uuid.UUID](ctx, s, sessionID,
		executionRequest{Operation: executionResume, ExecutionID: executionID})
}

func (s *Store) ListExecutionEvents(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	options session.ListEventsOptions,
) (*session.ExecutionEventPage, error) {
	return executionCall[*session.ExecutionEventPage](ctx, s, sessionID,
		executionRequest{
			Operation: executionEvents, ExecutionID: executionID, Page: options,
		})
}

func (s *Store) BeginExecutionActivity(
	ctx context.Context,
	sessionID, executionID, generationID uuid.UUID,
	input session.ScheduleActivityInput,
) (*session.ActivityClaim, error) {
	return executionCall[*session.ActivityClaim](ctx, s, sessionID,
		executionRequest{
			Operation: executionBeginActivity, ExecutionID: executionID,
			GenerationID: generationID, Begin: input,
		})
}

func (s *Store) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	return executionCall[*models.ExecutionActivity](ctx, s, sessionID,
		executionRequest{
			Operation: executionCompleteActivity, Lease: lease, Complete: input,
		})
}

func (s *Store) InterruptExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
) error {
	_, err := executionCall[struct{}](ctx, s, sessionID,
		executionRequest{Operation: executionInterruptActivity, Lease: lease})

	return err
}

func (s *Store) ResolveExecutionActivity(
	ctx context.Context,
	sessionID, activityID uuid.UUID,
	input session.ResolveActivityInput,
) (*models.ExecutionActivity, error) {
	return executionCall[*models.ExecutionActivity](ctx, s, sessionID,
		executionRequest{
			Operation: executionResolveActivity, ActivityID: activityID,
			Resolve: input,
		})
}

func (s *Store) GetExecutionActivity(
	ctx context.Context,
	sessionID, activityID uuid.UUID,
) (*models.ExecutionActivity, error) {
	return executionCall[*models.ExecutionActivity](ctx, s, sessionID,
		executionRequest{
			Operation: executionGetActivity, ActivityID: activityID,
		})
}

func (s *Store) ListExecutionActivities(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	options session.ListEventsOptions,
) (*session.ExecutionActivityPage, error) {
	return executionCall[*session.ExecutionActivityPage](ctx, s, sessionID,
		executionRequest{
			Operation: executionListActivities, ExecutionID: executionID,
			Page: options,
		})
}
