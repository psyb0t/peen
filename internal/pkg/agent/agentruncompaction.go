package agent

import (
	"context"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// childCompactionLeadCount is the child prompt's leading message count: the
// system prompt, which a child always has.
const childCompactionLeadCount = 1

// newChildCompactor returns the child's own compaction hook, or nil under
// drop-oldest, which keeps its existing non-durable behavior.
//
// The compactor is scoped to the child's transcript and its own compaction
// rows. Parent session messages and parent compaction rows are never read or
// written by it.
func (r *Runtime) newChildCompactor(
	ctx context.Context,
	deps *launchAgentDeps,
	run *AgentRun,
	systemPrompt string,
	hook compactionHook,
) (*compactor, error) {
	if r.compactionMode != config.CompactionModeSummarize {
		//nolint:nilnil // Drop-oldest deliberately installs no hook.
		return nil, nil
	}

	refresh := r.childPlanRefresher(deps.sessionID, run.ID, systemPrompt)

	plan, active, err := refresh(ctx)
	if err != nil {
		return nil, err
	}

	options := r.compactionOptions
	options.Workspace = deps.executor.Workspace()
	options.Hook = composeCompactionHooks(options.Hook, hook)

	built, err := newCompactor(
		options,
		deps.sessionID,
		deps.parentTurnID,
		plan,
		agentRunCompactionSink{
			store:      r.store,
			sessionID:  deps.sessionID,
			agentRunID: run.ID,
		},
		active,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create child compactor")
	}

	agentRunID := run.ID
	built.agentRunID = &agentRunID
	built.refresh = refresh

	return built, nil
}

// childPlanRefresher rereads one child's durable transcript and rebuilds its
// reconstruction plan and chain head.
//
// A child's whole conversation is written during its parent's turn, so the
// plan built when the compactor was created covers only the seeded task. Every
// compaction therefore replans against what the child has actually stored.
func (r *Runtime) childPlanRefresher(
	sessionID uuid.UUID,
	agentRunID uuid.UUID,
	systemPrompt string,
) planRefresher {
	return func(
		ctx context.Context,
	) (reconstructionPlan, *compactionHead, error) {
		history, err := r.store.AgentRunHistory(ctx, sessionID, agentRunID)
		if err != nil {
			return reconstructionPlan{}, nil, ctxerrors.Wrap(
				err,
				"load child history for compaction",
			)
		}

		plan, err := buildChildReconstructionPlan(
			childPlanLeadCount(systemPrompt, history),
			history.Compaction != nil,
			history.Messages,
		)
		if err != nil {
			return reconstructionPlan{}, nil, ctxerrors.Wrap(
				err,
				"build child reconstruction plan",
			)
		}

		return plan, childCompactionHead(history.Compaction), nil
	}
}

// childPlanLeadCount reports how many messages precede child history in the
// assembled prompt: the system message, plus the synthetic summary when the
// child already carries one.
func childPlanLeadCount(
	systemPrompt string,
	history *session.AgentRunHistoryResult,
) int {
	lead := 0
	if systemPrompt != "" {
		lead = childCompactionLeadCount
	}

	if history.Compaction != nil {
		lead++
	}

	return lead
}

// childCompactionHead adapts a child's stored chain head to the shape a
// compactor supersedes.
func childCompactionHead(
	active *models.AgentRunCompaction,
) *compactionHead {
	if active == nil {
		return nil
	}

	return &compactionHead{
		ID:                 active.ID,
		FromMessageID:      active.FromMessageID,
		FromSequence:       active.FromSequence,
		SourceMessageCount: active.SourceMessageCount,
	}
}

// buildChildReconstructionPlan groups a child transcript into whole
// tool-exchange units.
//
// A child has no turns, so each unit is given its own identity. That makes
// coverableUnits leave exactly the latest complete tool-exchange unit raw,
// which is the same guarantee a session turn gets from its turn boundary.
func buildChildReconstructionPlan(
	leadCount int,
	hasSummary bool,
	messages []*models.AgentRunMessage,
) (reconstructionPlan, error) {
	plan := reconstructionPlan{
		LeadCount:  leadCount,
		HasSummary: hasSummary,
		Units:      make([]reconstructionUnit, 0, len(messages)),
	}

	for _, message := range messages {
		if message == nil {
			return reconstructionPlan{}, ctxerrors.Wrap(
				ErrCompactionPlanMismatch,
				"nil child history message",
			)
		}

		role, err := elelemRole(message.Role)
		if err != nil {
			return reconstructionPlan{}, ctxerrors.Wrap(
				err,
				"classify child history message role",
			)
		}

		// A tool result belongs to the call that asked for it, so it extends
		// the open unit instead of starting one.
		if role == elelem.RoleTool && len(plan.Units) > 0 {
			unit := &plan.Units[len(plan.Units)-1]
			unit.ToMessageID = message.ID
			unit.ToSequence = message.Sequence
			unit.MessageCount++
			unit.Roles = append(unit.Roles, role)

			continue
		}

		plan.Units = append(plan.Units, reconstructionUnit{
			FromMessageID: message.ID,
			ToMessageID:   message.ID,
			FromSequence:  message.Sequence,
			ToSequence:    message.Sequence,
			TurnID:        message.ID,
			MessageCount:  1,
			Roles:         []elelem.Role{role},
		})
	}

	return plan, nil
}

// childCompactionHook runs one child compaction lifecycle hook with the
// child's own correlation scope, so a hook can tell child work from its
// parent's. The pre and post behavior is the tool-hook runtime's, the same one
// the child's tools already use.
func childCompactionHook(
	run *AgentRun,
	hooks *toolHookRuntime,
) compactionHook {
	if hooks == nil {
		return nil
	}

	return func(
		ctx context.Context,
		event harness.HookEvent,
		payload compactionHookPayload,
	) error {
		scoped := ctxscope.Set(
			ctx,
			ctxscope.Attr("agent_run_id", run.ID.String()),
			ctxscope.Attr("agent_name", run.Name),
		)

		outcome, err := hooks.runLifecycleWithContextTokens(
			scoped,
			event,
			payload,
			payload.EstimatedTokens,
		)
		if err != nil {
			return ctxerrors.Wrap(err, "run child compaction lifecycle hooks")
		}

		// A compaction hook cannot add messages to the transcript it is
		// summarizing, so an injection here is recorded and dropped, the same
		// way the parent turn treats one.
		if len(outcome.Injections) > 0 {
			ctxscope.GetLogger(scoped).Debug(
				"child compaction hook injections ignored",
				"hook_event", event,
				"injection_count", len(outcome.Injections),
				"agent_run_id", run.ID.String(),
			)
		}

		return nil
	}
}
