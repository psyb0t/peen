package agent

import (
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/config"
)

// withOutputLimit sends the output reserve as the request's output limit.
// Providers count reasoning inside it, so a reply can never outgrow the room
// the budget kept for it and overflow the request that replays it. A reserve
// of 0 sends none.
func withOutputLimit(
	request *elelem.Request,
	reserveTokens int,
) *elelem.Request {
	if reserveTokens <= 0 {
		return request
	}

	return request.WithMaxOutputTokens(int64(reserveTokens))
}

// ResolveContextBudget fills a deployment's 0 context and output reserve from
// its models, as ModelLimits reports them. The controller and every worker
// resolve the same settings against the same catalogs, so they agree on the
// budget.
func ResolveContextBudget(
	deployment config.Config,
	models ModelResolver,
) (config.Config, error) {
	window, maxOutput, err := ModelLimits(models, deployment.DefaultModel,
		deployment.CompactionModel)
	if err != nil {
		return deployment, err
	}

	resolved, err := deployment.ResolveContextBudget(window, maxOutput)
	if err != nil {
		return deployment, ctxerrors.Wrap(err, "resolve the context budget")
	}

	return resolved, nil
}

// ModelLimits returns the window a 0 context takes and the output limit a 0
// reserve takes. The window is the smaller of the default and compaction
// models' windows, because the compaction model reads the history a full
// context holds. The output limit is the default model's. An empty
// compactionModel is the default model. 0 means the models publish none.
func ModelLimits(
	models ModelResolver,
	defaultModel, compactionModel string,
) (int, int, error) {
	selected, err := models.ResolveModel(defaultModel)
	if err != nil {
		return 0, 0, ctxerrors.Wrap(err, settingDefaultModel)
	}

	if compactionModel == "" {
		compactionModel = defaultModel
	}

	compaction, err := models.ResolveModel(compactionModel)
	if err != nil {
		return 0, 0, ctxerrors.Wrap(err, settingCompactionModel)
	}

	window := smallestWindow(selected.Model.ContextSize,
		compaction.Model.ContextSize)

	return window, selected.Model.MaxOutputTokens, nil
}

// smallestWindow is the smaller of two published windows. A window of 0 is
// unpublished and does not count.
func smallestWindow(first, second int) int {
	if first <= 0 {
		return second
	}

	if second <= 0 {
		return first
	}

	return min(first, second)
}
