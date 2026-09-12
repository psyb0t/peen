package agent

import (
	"context"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
)

const (
	reasonInterruptedTurnsRecovered     = "interrupted_turns_recovered"
	reasonInterruptedAgentRunsRecovered = "interrupted_agent_runs_recovered"
	reasonInterruptedJobsRecovered      = "interrupted_jobs_recovered"
	reasonInterruptedModelRunsRecovered = "interrupted_model_runs_recovered"
)

// Assembled is one wired runtime and the resources whose lifetime it shares.
//
// The caller owns closing Handle. Store is returned because a service needs it
// for lifecycle work the runtime does not expose.
type Assembled struct {
	Runtime *Runtime
	Store   *session.Store
	Handle  *db.Handle
}

// AssembleOptions builds a runtime over one configuration directory.
type AssembleOptions struct {
	// Runtime carries the tuning and the model resolver. Store and Resolver
	// are filled in by Assemble, so a caller leaves them zero.
	Runtime RuntimeOptions

	// HarnessLimits bounds filesystem-derived context. Zero fields take the
	// harness package defaults.
	HarnessLimits harness.Limits

	// StoreOptions carries the store's own bounds and its clock and ID seams.
	StoreOptions session.Options
}

// Assemble opens the durable store, settles anything a previous process left
// running, and wires one runtime over both.
//
// This exists because there are two callers, the HTTP service and the public
// facade, and they had each grown their own copy of this sequence. Two
// construction paths for one runtime is how a setting gets wired in one place
// and forgotten in the other, which is the shape of most of the defects this
// project has had. Anything that must be true of every runtime, such as
// recovering interrupted turns before serving, belongs here so neither caller
// can omit it.
//
// The model resolver is deliberately NOT built here: a service discovers
// models from configured providers, and an embedding Go program supplies its
// own clients. That difference is real, so it stays with the callers.
func Assemble(
	ctx context.Context,
	options AssembleOptions,
) (*Assembled, error) {
	configDirectory := options.Runtime.ConfigDirectory

	handle, err := db.Open(ctx, db.Config{
		Directory: configDirectory,
		Metrics:   options.Runtime.Metrics,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "open durable state")
	}

	assembled, err := assembleOver(ctx, handle, options)
	if err != nil {
		return nil, closeHandleAfter(ctx, handle, err)
	}

	return assembled, nil
}

func assembleOver(
	ctx context.Context,
	handle *db.Handle,
	options AssembleOptions,
) (*Assembled, error) {
	resolver, err := harness.NewResolver(
		options.Runtime.ConfigDirectory,
		options.HarnessLimits,
	)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create harness resolver")
	}

	store, err := session.NewStore(handle, options.StoreOptions)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create session store")
	}

	if err := recoverInterruptedTurns(ctx, store); err != nil {
		return nil, err
	}
	if err := recoverInterruptedAgentRuns(ctx, store); err != nil {
		return nil, err
	}
	if err := recoverInterruptedJobs(ctx, store); err != nil {
		return nil, err
	}
	if err := recoverInterruptedModelRuns(ctx, store); err != nil {
		return nil, err
	}

	runtimeOptions := options.Runtime
	runtimeOptions.Store = store
	runtimeOptions.Resolver = resolver

	runtime, err := NewRuntime(runtimeOptions)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "create agent runtime")
	}

	return &Assembled{
		Runtime: runtime,
		Store:   store,
		Handle:  handle,
	}, nil
}

// recoverInterruptedTurns settles turns a previous process left running.
//
// This has to happen before the first request. A turn still marked running
// holds its session's lease against a lease map that starts empty after a
// restart, so nothing else would stop a second turn being acquired for a
// session that already has an orphaned one.
func recoverInterruptedTurns(ctx context.Context, store *session.Store) error {
	recovered, err := store.RecoverInterrupted(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "recover interrupted turns")
	}

	if recovered == 0 {
		return nil
	}

	ctxscope.GetLogger(ctx).Warn(
		"marked turns left running by a previous process as interrupted",
		"reason", reasonInterruptedTurnsRecovered,
		"turn_count", recovered,
	)

	return nil
}

func recoverInterruptedAgentRuns(ctx context.Context, store *session.Store) error {
	recovered, err := store.RecoverInterruptedAgentRuns(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "recover interrupted agent runs")
	}

	if recovered == 0 {
		return nil
	}

	ctxscope.GetLogger(ctx).Warn(
		"marked child agents left running by a previous process as interrupted",
		"reason", reasonInterruptedAgentRunsRecovered,
		"agent_run_count", recovered,
	)

	return nil
}

func recoverInterruptedJobs(ctx context.Context, store *session.Store) error {
	recovered, err := store.RecoverInterruptedJobs(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "recover interrupted jobs")
	}

	if recovered == 0 {
		return nil
	}

	ctxscope.GetLogger(ctx).Warn(
		"marked jobs left running by a previous process as interrupted",
		"reason", reasonInterruptedJobsRecovered,
		"job_count", recovered,
	)

	return nil
}

func recoverInterruptedModelRuns(ctx context.Context, store *session.Store) error {
	recovered, err := store.RecoverInterruptedModelRuns(ctx)
	if err != nil {
		return ctxerrors.Wrap(err, "recover interrupted model runs")
	}

	if recovered == 0 {
		return nil
	}

	ctxscope.GetLogger(ctx).Warn(
		"marked model work left running by a previous process as interrupted",
		"reason", reasonInterruptedModelRunsRecovered,
		"model_run_count", recovered,
	)

	return nil
}

// closeHandleAfter releases the database when assembly failed, joining any
// close failure onto the original error rather than hiding either.
func closeHandleAfter(
	ctx context.Context,
	handle *db.Handle,
	cause error,
) error {
	if err := handle.Close(); err != nil {
		ctxscope.GetLogger(ctx).Error(
			"closing durable state after a failed assembly failed",
			"err", err,
		)
	}

	return cause
}
