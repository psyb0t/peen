package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
)

type preparedChildConversation struct {
	scope           *directoryScope
	compactor       *compactor
	prompt          elelem.Prompt
	maximumRounds   int
	remainingRounds int
}

func (r *Runtime) prepareChildConversation(
	ctx context.Context,
	deps *launchAgentDeps,
	launch preparedChildLaunch,
	task string,
	run *AgentRun,
) (*preparedChildConversation, error) {
	if deps == nil || run == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child preparation dependencies")
	}

	scope, err := newDirectoryScope(ctx, directoryScopeOptions{
		runtime: r, deps: deps,
		childInstructions: launch.definition.instructions,
		allowedTools:      launch.definition.allowedTools,
		depth:             launch.depth, agentRunID: &run.ID,
		initialPrompt: launch.systemPrompt, skills: launch.skillLedger,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build child directory scope")
	}

	compactor, err := r.newChildCompactor(ctx, deps, run,
		launch.systemPrompt, scope.childCompactionHook(run))
	if err != nil {
		return nil, ctxerrors.Wrap(err, "build child compactor")
	}

	scope.bindChildCompactor(compactor)
	scope.completedRounds = launch.completedRounds
	scope.roundOffset = launch.completedRounds
	maximum := childConversationRounds(launch, r.agentLimits.MaxChildTurns)
	scope.maximumRounds = maximum

	prompt, remaining, err := childRequestPrompt(launch, task, maximum)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "prepare child request budget")
	}

	return &preparedChildConversation{
		scope: scope, compactor: compactor, prompt: prompt,
		maximumRounds: maximum, remainingRounds: remaining,
	}, nil
}
