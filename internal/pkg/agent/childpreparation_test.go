package agent

import (
	"context"
	"testing"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/stretchr/testify/require"
)

func TestChildPreparationRejectsMissingDependencies(t *testing.T) {
	t.Parallel()
	var runtime Runtime
	_, err := runtime.prepareChildConversation(t.Context(), nil,
		preparedChildLaunch{}, "example task", nil)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

func TestChildPreparationHonorsCancellationAndUsedBudget(t *testing.T) {
	t.Parallel()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := fixture.base.runtime.prepareChildConversation(ctx, deps,
		launch, "example task", run)
	require.ErrorIs(t, err, context.Canceled)
	launch.completedRounds = prepared.maximumRounds
	_, err = fixture.base.runtime.prepareChildConversation(t.Context(), deps,
		launch, "example task", run)
	require.ErrorIs(t, err, elelem.ErrMaxRoundsExceeded)
}
