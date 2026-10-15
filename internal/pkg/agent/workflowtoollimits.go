package agent

import (
	"context"
	"math"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
)

const (
	workflowToolTruncatedMarker    = "…[truncated]"
	workflowTokenizerBytesPerToken = 64
	workflowBinarySplitFactor      = 2
)

func boundedWorkflowToolResult(
	ctx context.Context,
	outcome workflowToolResult,
	options workflowToolOptions,
) (workflowToolResult, error) {
	content, err := boundWorkflowToolContent(
		ctx, outcome.Result.Content, options,
	)
	if err != nil {
		return outcome, err
	}

	outcome.Result.Content = content

	return outcome, nil
}

func boundWorkflowToolContent(
	ctx context.Context,
	content string,
	options workflowToolOptions,
) (string, error) {
	byteLimit := workflowToolByteLimit(options)

	wasClipped := byteLimit > 0 && len(content) > byteLimit
	if wasClipped {
		content = workflowUTF8Prefix(content, byteLimit)
	}

	if options.MaxResultTokens <= 0 {
		if wasClipped {
			return fitWorkflowToolBytes(content, byteLimit), nil
		}

		return content, nil
	}

	counter := options.Counter
	if counter == nil {
		counter = elelem.DefaultTokenCounter()
	}

	count, err := workflowContentTokens(counter, content)
	if err != nil {
		return "", err
	}

	if !wasClipped && count <= options.MaxResultTokens {
		return content, nil
	}

	return fitWorkflowToolTokens(ctx, counter, content, options, byteLimit)
}

func workflowToolByteLimit(options workflowToolOptions) int {
	byteLimit := options.MaxResultBytes
	if options.MaxResultTokens <= 0 ||
		options.MaxResultTokens > math.MaxInt/workflowTokenizerBytesPerToken {
		return byteLimit
	}

	tokenLimit := options.MaxResultTokens * workflowTokenizerBytesPerToken
	if byteLimit <= 0 {
		return tokenLimit
	}

	return min(byteLimit, tokenLimit)
}

func workflowUTF8Prefix(content string, limit int) string {
	if len(content) <= limit {
		return content
	}

	for limit > 0 && !utf8.RuneStart(content[limit]) {
		limit--
	}

	return content[:limit]
}

func fitWorkflowToolBytes(content string, limit int) string {
	if limit < len(workflowToolTruncatedMarker) {
		return workflowUTF8Prefix(workflowToolTruncatedMarker, limit)
	}

	return workflowUTF8Prefix(content,
		limit-len(workflowToolTruncatedMarker)) + workflowToolTruncatedMarker
}

func workflowContentTokens(
	counter elelem.TokenCounter,
	content string,
) (int, error) {
	baseline, err := counter.Count(
		[]elelem.Message{{Role: elelem.RoleTool}}, nil,
	)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count empty tool result")
	}

	count, err := counter.Count([]elelem.Message{{
		Role: elelem.RoleTool, Content: elelem.Text(content),
	}}, nil)
	if err != nil {
		return 0, ctxerrors.Wrap(err, "count tool result tokens")
	}

	return max(0, count-baseline), nil
}

func fitWorkflowToolTokens(
	ctx context.Context,
	counter elelem.TokenCounter,
	content string,
	options workflowToolOptions,
	byteLimit int,
) (string, error) {
	marker, err := workflowToolMarker(
		counter, options.MaxResultTokens, byteLimit,
	)
	if err != nil {
		return "", err
	}

	best := marker

	low, high := 0, len(content)
	for low <= high {
		if err := ctx.Err(); err != nil {
			return "", ctxerrors.Wrap(err, "fit tool result cancelled")
		}

		middle := low + (high-low)/workflowBinarySplitFactor

		candidate := workflowUTF8Prefix(content, middle) + marker
		if byteLimit > 0 && len(candidate) > byteLimit {
			high = middle - 1

			continue
		}

		count, err := workflowContentTokens(counter, candidate)
		if err != nil {
			return "", err
		}

		if count > options.MaxResultTokens {
			high = middle - 1

			continue
		}

		best = candidate
		low = middle + 1
	}

	return best, nil
}

func workflowToolMarker(
	counter elelem.TokenCounter,
	maxTokens, byteLimit int,
) (string, error) {
	marker := workflowToolTruncatedMarker
	if byteLimit > 0 {
		marker = workflowUTF8Prefix(marker, byteLimit)
	}

	for marker != "" {
		count, err := workflowContentTokens(counter, marker)
		if err != nil {
			return "", err
		}

		if count <= maxTokens {
			return marker, nil
		}

		marker = workflowUTF8Prefix(marker, len(marker)-1)
	}

	return marker, nil
}
