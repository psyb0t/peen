package control

import (
	"context"
	"math"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// OpenWorkspaceSession resolves a requested workspace to its durable session
// and reports whether this call created it.
//
// It returns commerr.ErrPermissionDenied when the path falls outside every
// configured workspace root or names an execution profile the deployment does
// not define, and commerr.ErrValidationFailed when the path is not a usable
// directory. None of those cases creates a session.
func (r *Registry) OpenWorkspaceSession(
	ctx context.Context,
	workspace string,
	profileName string,
) (api.OpenedSession, error) {
	opened, err := r.Open(ctx, workspace, profileName)
	if err != nil {
		return api.OpenedSession{}, err
	}

	return api.OpenedSession{
		Created: opened.Created,
		Session: agent.SessionToAPI(
			opened.Session,
			r.store.IsActive(opened.Session.ID),
		),
	}, nil
}

// ListSessions returns one bounded page of the sessions this control surface
// holds, newest-updated first.
func (r *Registry) ListSessions(
	ctx context.Context,
	params api.ListSessionsParams,
) (api.SessionPage, error) {
	page, err := r.store.ListSessions(ctx, session.ListSessionsOptions{
		Limit:  optionalPageBound(params.Limit),
		Offset: optionalPageBound(params.Offset),
	})
	if err != nil {
		return api.SessionPage{}, ctxerrors.Wrap(err, "list sessions")
	}

	items := make([]api.Session, 0, len(page.Items))
	for _, stored := range page.Items {
		items = append(
			items,
			agent.SessionToAPI(stored, r.store.IsActive(stored.ID)),
		)
	}

	limit, err := pageValueToAPI(page.Limit, "session page limit")
	if err != nil {
		return api.SessionPage{}, err
	}

	offset, err := pageValueToAPI(page.Offset, "session page offset")
	if err != nil {
		return api.SessionPage{}, err
	}

	return api.SessionPage{
		Items:   items,
		Limit:   limit,
		Offset:  offset,
		HasMore: page.HasMore,
	}, nil
}

// optionalPageBound converts an absent API paging value into the zero the
// store reads as "use the default bound".
func optionalPageBound(value *int32) int {
	if value == nil {
		return 0
	}

	return int(*value)
}

// pageValueToAPI narrows a store page bound to the API's int32 rather than
// letting an out-of-range value wrap silently.
func pageValueToAPI(value int, field string) (int32, error) {
	if value < 0 || value > math.MaxInt32 {
		return 0, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"%s is out of range",
			field,
		)
	}

	return int32(value), nil
}
