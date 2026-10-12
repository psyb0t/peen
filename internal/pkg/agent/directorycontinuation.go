package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

func (s *directoryScope) beforeRound(
	_ context.Context,
	event *elelem.RoundEvent,
) error {
	current := s.context()
	if current != s.applied {
		return errDirectoryChanged
	}

	event.Round += s.roundOffset
	event.MaxRounds = s.maximumRounds

	return nil
}

func (s *directoryScope) afterRound(
	_ context.Context,
	_ *elelem.RoundEvent,
) error {
	s.completedRounds++

	return nil
}

// run keeps one deadline and round budget across scope replacements. A pause
// occurs before a provider call, after the prior round's results are paired.
func (s *directoryScope) run(
	ctx context.Context,
	request *elelem.Request,
	maximumRounds int,
	timeout time.Duration,
) (*elelem.Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	request.WithTimeout(0)

	s.maximumRounds = maximumRounds

	var (
		usage            elelem.Usage
		injections       []elelem.MessageInjection
		reasoning        []string
		cost             float64
		segmentReasoning []string
	)

	request.OnReasoning(collectDirectoryReasoning(&segmentReasoning))

	for {
		segmentReasoning = nil

		response, err := request.Run(ctx)
		if response != nil {
			usage = mergeDirectoryUsage(usage, response.Usage)
			injections = append(injections, response.Injections...)
			reasoning = append(
				reasoning, directoryReasoning(err, segmentReasoning, response),
			)
			cost += response.Cost
			response.Usage = usage
			response.Injections = injections
			response.Reasoning = strings.Join(reasoning, "")
			response.Cost = cost
		}

		if !errors.Is(err, errDirectoryChanged) || response == nil {
			if err != nil {
				return response, ctxerrors.Wrap(err, "run directory request")
			}

			return response, nil
		}

		current, remaining, err := s.advance(maximumRounds)
		if err != nil {
			return response, ctxerrors.Wrap(err, "continue directory request")
		}

		prompt := elelem.NewPrompt().WithSystem(current.prompt).
			WithHistory(directoryHistory(response.Messages))
		request.WithPrompt(prompt).WithMaxRounds(remaining)
	}
}

func collectDirectoryReasoning(
	segments *[]string,
) func(context.Context, elelem.ReasoningDelta) error {
	return func(_ context.Context, delta elelem.ReasoningDelta) error {
		*segments = append(*segments, delta.Text)

		return nil
	}
}

func directoryReasoning(
	err error,
	segments []string,
	response *elelem.Response,
) string {
	// Elelem's partial response retains messages but omits reasoning.
	if errors.Is(err, errDirectoryChanged) {
		return strings.Join(segments, "")
	}

	return response.Reasoning
}

func (s *directoryScope) advance(
	maximumRounds int,
) (*directoryContext, int, error) {
	current := s.context()
	s.applied = current
	s.roundOffset = s.completedRounds
	s.applyContext(current)

	remaining := maximumRounds - s.completedRounds
	if remaining <= 0 {
		return nil, 0, ctxerrors.Wrap(
			elelem.ErrMaxRoundsExceeded, "directory continuation budget",
		)
	}

	return current, remaining, nil
}

func directoryHistory(messages []elelem.Message) []elelem.Message {
	history := make([]elelem.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != elelem.RoleSystem {
			history = append(history, message)
		}
	}

	return history
}

func (s *directoryScope) applyContext(current *directoryContext) {
	if s.options.apply != nil {
		s.options.apply(current)
	}
}

func mergeDirectoryUsage(total, next elelem.Usage) elelem.Usage {
	total.Prompt += next.Prompt
	total.Completion += next.Completion
	total.Total += next.Total
	total.Reasoning += next.Reasoning
	total.CacheRead += next.CacheRead
	total.CacheWrite += next.CacheWrite

	total.CacheWriteLongTTL += next.CacheWriteLongTTL
	if next.Model != "" {
		total.Model = next.Model
	}

	total.FinishReason = next.FinishReason
	total.Retry.TotalAttempts += next.Retry.TotalAttempts
	total.Retry.FailedAttempts = append(
		total.Retry.FailedAttempts, next.Retry.FailedAttempts...,
	)
	total.Retry.WastedPromptTokens += next.Retry.WastedPromptTokens
	total.Retry.WastedCompletionTokens += next.Retry.WastedCompletionTokens
	total.Retry.WastedTotalTokens += next.Retry.WastedTotalTokens

	return total
}
