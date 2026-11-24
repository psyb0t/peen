package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListCronJobs(
	ctx context.Context,
	_ api.ListCronJobsRequestObject,
) (api.ListCronJobsResponseObject, error) {
	jobs := api.CronJobList{Items: []api.CronJob{}}

	if s.deps.Cron != nil {
		var err error

		jobs, err = s.deps.Cron.ListCronJobs(ctx)
		if err != nil {
			return nil, ctxerrors.Wrap(err, "list scheduled jobs")
		}
	}

	return api.ListCronJobs200JSONResponse{
		Body:    jobs,
		Headers: api.ListCronJobs200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}

//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListCronRuns(
	ctx context.Context,
	request api.ListCronRunsRequestObject,
) (api.ListCronRunsResponseObject, error) {
	page := api.CronRunPage{
		Items: []api.CronRun{},
		Limit: session.DefaultPageLimit,
	}
	if request.Params.Limit != nil {
		page.Limit = *request.Params.Limit
	}

	if request.Params.Offset != nil {
		page.Offset = *request.Params.Offset
	}

	if s.deps.Cron != nil {
		var err error

		page, err = s.deps.Cron.ListCronRuns(ctx, request.Params)
		if errors.Is(err, commerr.ErrValidationFailed) {
			return api.ListCronRuns400JSONResponse{
				ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
					validationError("invalid cron history page")),
			}, nil
		}

		if err != nil {
			return nil, ctxerrors.Wrap(err, "list scheduled runs")
		}
	}

	return api.ListCronRuns200JSONResponse{
		Body:    page,
		Headers: api.ListCronRuns200ResponseHeaders{XRequestID: requestID(ctx)},
	}, nil
}
