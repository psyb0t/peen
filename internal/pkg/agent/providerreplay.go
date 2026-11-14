package agent

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

type providerReplayContextKey struct{}

// A fresh Elelem run discards its partial assistant accumulator. The activity
// and audit call stay open, and transport retries share this remaining budget.
type providerReplay struct {
	config elelem.RetryConfig
	usage  elelem.RetryInfo
	cost   float64
}

func providerReplayFrom(ctx context.Context) *providerReplay {
	replay, _ := ctx.Value(providerReplayContextKey{}).(*providerReplay)

	return replay
}

type providerRetryDriver struct {
	elelem.Driver
	config elelem.RetryConfig
}

//nolint:ireturn // Elelem's driver contract is an interface.
func (d providerRetryDriver) retryDriver(ctx context.Context) elelem.Driver {
	config := d.config
	if replay := providerReplayFrom(ctx); replay != nil {
		replay.config = config
		config.MaxAttempts -= replay.usage.TotalAttempts
		config.InitialDelay = providerRetryDelay(config,
			replay.usage.TotalAttempts+1, false)
	}

	return elelem.WithRetry(d.Driver, config)
}

func (d providerRetryDriver) Stream(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	usage, err := d.retryDriver(ctx).Stream(ctx, request, onDelta)
	if err != nil {
		return usage, ctxerrors.Wrap(err, "retry provider stream")
	}

	return usage, nil
}

func (d providerRetryDriver) Complete(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	usage, err := d.retryDriver(ctx).Complete(ctx, request, onDelta)
	if err != nil {
		return usage, ctxerrors.Wrap(err, "retry provider completion")
	}

	return usage, nil
}

func providerRetryDelay(
	config elelem.RetryConfig,
	attempt int,
	jitter bool,
) time.Duration {
	delay := config.InitialDelay
	for index := 1; index < attempt; index++ {
		if delay >= config.MaxDelay/providerRetryDelayDivisor {
			return config.MaxDelay
		}

		delay *= providerRetryDelayDivisor
	}

	delay = min(delay, config.MaxDelay)
	if jitter && (config.Jitter == nil || *config.Jitter) {
		half := delay / providerRetryDelayDivisor
		//nolint:gosec // Backoff jitter carries no security decision.
		delay = half + time.Duration(rand.Float64()*float64(half))
	}

	return delay
}

func providerReplayAttempt(
	ctx context.Context,
	attempt elelem.RetryAttempt,
) elelem.RetryAttempt {
	if replay := providerReplayFrom(ctx); replay != nil {
		attempt.Attempt += replay.usage.TotalAttempts
	}

	return attempt
}

func providerReplayUsage(ctx context.Context, usage elelem.Usage) elelem.Usage {
	replay := providerReplayFrom(ctx)
	if replay == nil {
		return usage
	}

	usage.Retry.FailedAttempts = append([]elelem.RetryAttempt(nil),
		usage.Retry.FailedAttempts...)

	for index := range usage.Retry.FailedAttempts {
		usage.Retry.FailedAttempts[index].Attempt += replay.usage.TotalAttempts
	}

	usage.Retry.TotalAttempts += replay.usage.TotalAttempts
	usage.Retry.FailedAttempts = append(
		append([]elelem.RetryAttempt(nil), replay.usage.FailedAttempts...),
		usage.Retry.FailedAttempts...)
	usage.Retry.WastedPromptTokens += replay.usage.WastedPromptTokens
	usage.Retry.WastedCompletionTokens += replay.usage.WastedCompletionTokens
	usage.Retry.WastedTotalTokens += replay.usage.WastedTotalTokens

	return usage
}

func runProviderReplay(
	ctx context.Context,
	request *elelem.Request,
	timeout time.Duration,
	onRetry func(context.Context, elelem.RetryAttempt) error,
	closeStream func(context.Context) error,
) (*elelem.Response, error) {
	if closeStream == nil {
		closeStream = func(context.Context) error { return nil }
	}

	replay := &providerReplay{config: providerRetryConfig()}
	ctx = context.WithValue(ctx, providerReplayContextKey{}, replay)

	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	request.WithTimeout(0)

	for {
		response, err := request.Run(ctx)
		if response != nil {
			response.Usage = providerReplayUsage(ctx, response.Usage)
			response.Cost += replay.cost
		}

		attempt, retry := replay.nextAttempt(ctx, response, err)
		if !retry {
			return response, ctxerrors.Wrap(err, "run provider attempt")
		}

		if closeErr := closeStream(ctx); closeErr != nil {
			return response, ctxerrors.Wrap(errors.Join(err, closeErr),
				"close cut stream before retry")
		}

		usage := response.Usage.Retry
		usage.FailedAttempts[len(usage.FailedAttempts)-1] = attempt
		// The callback receives an absolute attempt, outside Elelem's local
		// retry sequence, so it must not apply the prior offset again.
		replay.usage = elelem.RetryInfo{}

		if retryErr := onRetry(ctx, attempt); retryErr != nil {
			return response, ctxerrors.Wrap(retryErr, "record cut stream retry")
		}

		replay.usage = usage
		replay.cost = response.Cost

		if waitErr := waitProviderReplay(ctx, attempt.Delay); waitErr != nil {
			return response, waitErr
		}
	}
}

func (r *providerReplay) nextAttempt(
	ctx context.Context,
	response *elelem.Response,
	err error,
) (elelem.RetryAttempt, bool) {
	if !errors.Is(err, errProviderStreamCut) || response == nil ||
		ctx.Err() != nil {
		return elelem.RetryAttempt{}, false
	}

	usage := response.Usage.Retry
	if usage.TotalAttempts >= r.config.MaxAttempts ||
		len(usage.FailedAttempts) == 0 {
		return elelem.RetryAttempt{}, false
	}

	attempt := usage.FailedAttempts[len(usage.FailedAttempts)-1]
	attempt.Delay = providerRetryDelay(r.config, usage.TotalAttempts, true)

	return attempt, attempt.Streamed
}

func waitProviderReplay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctxerrors.Wrap(ctx.Err(), "wait for cut stream retry")
	case <-timer.C:
		return nil
	}
}
