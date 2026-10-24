package client

import (
	"context"
	"errors"
	"time"

	"github.com/psyb0t/ctxerrors"

	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/internal/workflows"
)

const expirationWorkflowInstanceID = "expiration"

// StartAutoExpiration starts a system workflow that will automatically expire workflow instances.
//
// The workflow will run every `delay` and remove all workflow instances finished before Now() - `delay`.
func (c *Client) StartAutoExpiration(ctx context.Context, delay time.Duration) error {
	if !c.backend.FeatureSupported(backend.Feature_Expiration) {
		return backend.ErrNotSupported{}
	}

	_, err := c.CreateWorkflowInstance(ctx, WorkflowInstanceOptions{
		InstanceID: expirationWorkflowInstanceID,
	}, workflows.ExpireWorkflowInstances, delay)
	if err != nil {
		if errors.Is(err, backend.ErrInstanceAlreadyExists) {
			err = c.SignalWorkflow(ctx, expirationWorkflowInstanceID, workflows.UpdateExpirationSignal, delay)
			if err != nil {
				return ctxerrors.Wrap(err, "updating expiration workflow")
			}

			return nil
		}

		return ctxerrors.Wrap(err, "starting expiration workflow")
	}

	return nil
}
