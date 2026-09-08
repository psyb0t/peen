package db

import (
	"database/sql"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"gorm.io/gorm"
)

const (
	databaseMetricStartKey = "peen:database_metric_start"
	databaseMetricStartID  = "peen:metrics:start:"
	databaseMetricFinishID = "peen:metrics:finish:"

	gormCreateCallback = "gorm:create"
	gormQueryCallback  = "gorm:query"
	gormUpdateCallback = "gorm:update"
	gormDeleteCallback = "gorm:delete"
	gormRowCallback    = "gorm:row"
	gormRawCallback    = "gorm:raw"
	gormAfterQuery     = "gorm:after_query"
	gormTransactionEnd = "gorm:commit_or_rollback_transaction"

	databaseOperationCreate = "create"
	databaseOperationQuery  = "query"
	databaseOperationUpdate = "update"
	databaseOperationDelete = "delete"
	databaseOperationRow    = "row"
	databaseOperationRaw    = "raw"
)

type databaseMetricStart struct {
	operation string
	startedAt time.Time
}

type databaseMetricCallbacks struct {
	start  databaseMetricRegister
	finish databaseMetricRegister
}

type databaseMetricRegister func(string, func(*gorm.DB)) error

// installDatabaseMetrics registers callbacks around every GORM operation. The
// callbacks are passive: they read the result and pool state after GORM has
// completed its own work without changing query execution or error handling.
//
//nolint:funlen // The registrations define one complete callback matrix.
func installDatabaseMetrics(
	database *gorm.DB,
	sqlDB *sql.DB,
	collector *metrics.Metrics,
) error {
	if collector == nil {
		return nil
	}

	collector.DatabasePool(sqlDB.Stats())

	registrations := []struct {
		operation string
		callbacks databaseMetricCallbacks
	}{
		{
			operation: databaseOperationCreate,
			callbacks: databaseMetricCallbacks{
				start: database.Callback().
					Create().
					Before(gormCreateCallback).Register,
				finish: database.Callback().Create().After(
					gormTransactionEnd,
				).Register,
			},
		},
		{
			operation: databaseOperationQuery,
			callbacks: databaseMetricCallbacks{
				start: database.Callback().
					Query().
					Before(gormQueryCallback).Register,
				finish: database.Callback().
					Query().
					After(gormAfterQuery).Register,
			},
		},
		{
			operation: databaseOperationUpdate,
			callbacks: databaseMetricCallbacks{
				start: database.Callback().
					Update().
					Before(gormUpdateCallback).Register,
				finish: database.Callback().Update().After(
					gormTransactionEnd,
				).Register,
			},
		},
		{
			operation: databaseOperationDelete,
			callbacks: databaseMetricCallbacks{
				start: database.Callback().
					Delete().
					Before(gormDeleteCallback).Register,
				finish: database.Callback().Delete().After(
					gormTransactionEnd,
				).Register,
			},
		},
		{
			operation: databaseOperationRow,
			callbacks: databaseMetricCallbacks{
				start: database.Callback().
					Row().
					Before(gormRowCallback).Register,
				finish: database.Callback().
					Row().
					After(gormRowCallback).Register,
			},
		},
		{
			operation: databaseOperationRaw,
			callbacks: databaseMetricCallbacks{
				start: database.Callback().
					Raw().
					Before(gormRawCallback).Register,
				finish: database.Callback().
					Raw().
					After(gormRawCallback).Register,
			},
		},
	}

	for _, registration := range registrations {
		if err := registration.callbacks.start(
			databaseMetricStartID+registration.operation,
			startDatabaseMetric(registration.operation),
		); err != nil {
			return ctxerrors.Wrap(
				err,
				"register database metric start callback",
			)
		}

		if err := registration.callbacks.finish(
			databaseMetricFinishID+registration.operation,
			finishDatabaseMetric(collector, sqlDB),
		); err != nil {
			return ctxerrors.Wrap(
				err,
				"register database metric finish callback",
			)
		}
	}

	return nil
}

func startDatabaseMetric(operation string) func(*gorm.DB) {
	return func(database *gorm.DB) {
		database.InstanceSet(databaseMetricStartKey, databaseMetricStart{
			operation: operation,
			startedAt: time.Now(),
		})
	}
}

func finishDatabaseMetric(
	collector *metrics.Metrics,
	sqlDB *sql.DB,
) func(*gorm.DB) {
	return func(database *gorm.DB) {
		value, ok := database.InstanceGet(databaseMetricStartKey)
		if !ok {
			return
		}

		started, ok := value.(databaseMetricStart)
		if !ok {
			return
		}

		outcome := metrics.OutcomeSuccess
		if database.Error != nil {
			outcome = metrics.OutcomeError
		}

		collector.DatabaseCompleted(
			started.operation,
			database.Statement.Table,
			outcome,
			time.Since(started.startedAt),
		)
		collector.DatabasePool(sqlDB.Stats())
	}
}
