package control

import (
	"context"

	"github.com/psyb0t/peen/internal/pkg/http/api"
)

// CronReader exposes schedule metadata and durable history without dispatch.
type CronReader interface {
	ListCronJobs(context.Context) (api.CronJobList, error)
	ListCronRuns(context.Context, api.ListCronRunsParams) (
		api.CronRunPage, error,
	)
}

// CronScheduler shares the controller's lifetime and never exposes its store
// or dispatch operations to a worker.
type CronScheduler interface {
	CronReader
	Run(context.Context) error
	Close()
}
