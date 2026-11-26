package cronstore

import (
	"encoding/hex"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

func validateName(name string) error {
	if len(name) == 0 || len(name) > maxJobNameBytes {
		return invalidInput()
	}

	for _, value := range name {
		if validNameCharacter(value) {
			continue
		}

		return invalidInput()
	}

	return nil
}

func validNameCharacter(value rune) bool {
	return (value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9') || value == '_' || value == '-'
}

func validateJob(job *models.CronJob) error {
	if job == nil || job.NextScheduledAt.IsZero() ||
		len(job.DefinitionHash) != definitionHashBytes {
		return invalidInput()
	}

	if err := validateName(job.JobName); err != nil {
		return err
	}

	if _, err := hex.DecodeString(job.DefinitionHash); err != nil {
		return invalidInput()
	}

	return nil
}

func validateRun(run *models.CronRun) error {
	if run == nil || !validRunIdentity(run) {
		return invalidInput()
	}

	if err := validateName(run.JobName); err != nil {
		return err
	}

	if !validRunState(run) || !validRunTimes(run) {
		return invalidInput()
	}

	return nil
}

func validRunIdentity(run *models.CronRun) bool {
	return run.ID != uuid.Nil && !run.ScheduledAt.IsZero() &&
		validSession(run.SessionID) && validError(run.Error)
}

func validRunState(run *models.CronRun) bool {
	if run.State == models.CronRunStateRunning {
		return run.FinishedAt == nil && run.Error == "" && run.StartedAt != nil
	}

	return terminalState(run.State) && run.FinishedAt != nil &&
		(run.State == models.CronRunStateSkipped || run.StartedAt != nil)
}

func validRunTimes(run *models.CronRun) bool {
	if run.StartedAt != nil && run.StartedAt.IsZero() {
		return false
	}

	if run.FinishedAt != nil && run.FinishedAt.IsZero() {
		return false
	}

	return run.StartedAt == nil || run.FinishedAt == nil ||
		!run.FinishedAt.Before(*run.StartedAt)
}

func terminalState(state models.CronRunState) bool {
	return slices.Contains([]models.CronRunState{
		models.CronRunStateCompleted, models.CronRunStateFailed,
		models.CronRunStateCancelled, models.CronRunStateSkipped,
	}, state)
}

func validError(value string) bool {
	return len(value) <= maxErrorBytes && slices.Contains([]string{
		"", ErrorRunFailed, ErrorTurnFailed, ErrorTurnCancelled,
		ErrorTurnTimeout,
		ErrorPreviousRunning, ErrorWorkspaceBusy, ErrorControllerOffline,
		ErrorInterrupted,
		ErrorWorkspaceUnavailable, ErrorSessionUnavailable, ErrorOutputInvalid,
		ErrorAgentSpaceConflict,
	}, value)
}

func validSession(id *uuid.UUID) bool {
	return id == nil || *id != uuid.Nil
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}

	result := value.UTC()

	return &result
}

func invalidInput() error {
	return ctxerrors.Wrap(
		commerr.ErrValidationFailed, "invalid cron store input",
	)
}
