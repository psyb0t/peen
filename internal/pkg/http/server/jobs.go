package server

import (
	"context"
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
)

// ListSessionJobs reports the commands this session has started.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ListSessionJobs(
	ctx context.Context,
	request api.ListSessionJobsRequestObject,
) (api.ListSessionJobsResponseObject, error) {
	page, err := s.deps.Runtime.ListSessionJobs(
		ctx,
		request.Params.XSessionID,
		request.Params,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ListSessionJobs404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "list session jobs")
	}

	return api.ListSessionJobs200JSONResponse{
		Body: *page,
		Headers: api.ListSessionJobs200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// ReadSessionJobOutput returns a bounded window of one job's output.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) ReadSessionJobOutput(
	ctx context.Context,
	request api.ReadSessionJobOutputRequestObject,
) (api.ReadSessionJobOutputResponseObject, error) {
	output, err := s.deps.Runtime.ReadSessionJobOutput(
		ctx,
		request.Params.XSessionID,
		request.JobId,
		request.Params,
	)
	if err != nil {
		if errors.Is(err, commerr.ErrNotFound) {
			return api.ReadSessionJobOutput404JSONResponse{
				ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
					notFoundError(),
				),
			}, nil
		}

		return nil, ctxerrors.Wrap(err, "read session job output")
	}

	return api.ReadSessionJobOutput200JSONResponse{
		Body: *output,
		Headers: api.ReadSessionJobOutput200ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

// SignalSessionJob stops one running job.
//
//nolint:ireturn // Generated strict handler response interface.
func (s *Server) SignalSessionJob(
	ctx context.Context,
	request api.SignalSessionJobRequestObject,
) (api.SignalSessionJobResponseObject, error) {
	if request.Body == nil || request.Body.Signal == "" {
		return signalSessionJobBadRequest(jobSignalRequiredMessage), nil
	}

	result, err := s.deps.Runtime.SignalSessionJob(
		ctx,
		request.Params.XSessionID,
		request.JobId,
		*request.Body,
	)
	if err != nil {
		return signalSessionJobFailure(err)
	}

	return api.SignalSessionJob202JSONResponse{
		Body: *result,
		Headers: api.SignalSessionJob202ResponseHeaders{
			XRequestID: requestID(ctx),
			XSessionID: request.Params.XSessionID,
		},
	}, nil
}

//nolint:ireturn // Generated strict handler response interface.
func signalSessionJobFailure(
	err error,
) (api.SignalSessionJobResponseObject, error) {
	switch {
	case errors.Is(err, commerr.ErrNotFound):
		return api.SignalSessionJob404JSONResponse{
			ErrorNotFoundJSONResponse: api.ErrorNotFoundJSONResponse(
				notFoundError(),
			),
		}, nil
	case errors.Is(err, commerr.ErrValidationFailed):
		return signalSessionJobBadRequest(clientMessage(err)), nil
	default:
		return nil, ctxerrors.Wrap(err, "signal session job")
	}
}

func signalSessionJobBadRequest(
	message string,
) api.SignalSessionJob400JSONResponse {
	return api.SignalSessionJob400JSONResponse{
		ErrorBadRequestJSONResponse: api.ErrorBadRequestJSONResponse(
			validationError(message),
		),
	}
}
