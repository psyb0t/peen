package cronscheduler

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// checkAgentSpaces refuses startup when a job names an agent space other than
// its workspace's. The sessions of one workspace share one agent space, so
// such a job could never run. A workspace without sessions takes the first
// job's named agent space, and another job naming a different one conflicts
// with it. A job that names none follows its workspace.
func (s *Scheduler) checkAgentSpaces(ctx context.Context) error {
	type claim struct {
		space string
		owner string
	}

	claims := make(map[string]claim)

	for _, job := range s.jobs {
		definition := job.definition
		if definition.AgentSpace == "" {
			continue
		}

		held, known := claims[definition.Workspace]
		if !known {
			current, found, err := s.options.Sessions.WorkspaceAgentSpace(ctx,
				definition.Workspace)
			if err != nil {
				return ctxerrors.Wrapf(err,
					"read cron job %q workspace agent space", definition.Name)
			}

			held = claim{space: current}
			if !found {
				held = claim{
					space: definition.AgentSpace,
					owner: definition.Name,
				}
			}

			claims[definition.Workspace] = held
		}

		if held.space == definition.AgentSpace {
			continue
		}

		if held.owner != "" {
			return ctxerrors.Wrapf(commerr.ErrConflict,
				"cron job %q agentSpace %q conflicts with cron job %q agentSpace %q in workspace %q", //nolint:lll // One operator-facing sentence.
				definition.Name, definition.AgentSpace,
				held.owner, held.space, definition.Workspace)
		}

		return ctxerrors.Wrapf(commerr.ErrConflict,
			"cron job %q agentSpace %q conflicts with workspace %q, which runs in agent space %q", //nolint:lll // One operator-facing sentence.
			definition.Name, definition.AgentSpace,
			definition.Workspace, held.space)
	}

	return nil
}
