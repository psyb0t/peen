package agent

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/publicerror"
)

const (
	providerRefusalFormat = "The provider refused the request: %s."
	// providerRefusalStatusFormat names an HTTP status and its standard text.
	providerRefusalStatusFormat = "HTTP %d %s"

	providerRefusalQuota     = "the provider quota is exhausted"
	providerRefusalContext   = "the request exceeds the model's context window"
	providerRefusalAuth      = "authentication failed"
	providerRefusalRateLimit = "the provider is rate limiting requests"
	providerRefusalMissing   = "the model or endpoint was not found"
	providerRefusalDefault   = "the request was not accepted"
	providerRefusalStreamCut = "the stream ended without a finish reason"
)

// providerRefusalSentinels are the failures elelem has already decided not to
// retry, or retried until its attempts ran out. Running the model activity
// again cannot change them.
//
//nolint:gochecknoglobals // A fixed list the classifier reads.
var providerRefusalSentinels = []error{
	errProviderStreamCut,
	commerr.ErrNotAuthenticated,
	commerr.ErrNotFound,
	commerr.ErrRateLimited,
	commerr.ErrValidationFailed,
	commerr.ErrInvalidArgument,
	elelem.ErrContextExceeded,
	elelem.ErrQuotaExhausted,
}

// providerRefusalError is a provider answer the model activity will not
// repeat. Its text names the refusal without the provider's body, which stays
// in the model call record.
type providerRefusalError struct {
	text  string
	cause error
}

var _ publicerror.Public = (*providerRefusalError)(nil)

func newProviderRefusalError(cause error) *providerRefusalError {
	reason := providerRefusalReason(cause)

	return &providerRefusalError{
		text:  fmt.Sprintf(providerRefusalFormat, reason),
		cause: cause,
	}
}

func (e *providerRefusalError) Error() string {
	return e.text
}

// PublicMessage returns the text written for the reader.
func (e *providerRefusalError) PublicMessage() string {
	return e.text
}

func (e *providerRefusalError) Unwrap() error {
	return e.cause
}

// providerHTTPStatus returns the HTTP status a provider error carries, or 0.
func providerHTTPStatus(err error) int {
	var statusError elelem.HTTPStatusError
	if !errors.As(err, &statusError) {
		return 0
	}

	return statusError.HTTPStatus()
}

// isProviderRefusal reports a model failure that elelem classified as final:
// an HTTP error status, or one of the sentinels a driver joins onto it.
func isProviderRefusal(err error) bool {
	if providerHTTPStatus(err) >= http.StatusBadRequest {
		return true
	}

	for _, sentinel := range providerRefusalSentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	return false
}

func providerRefusalReason(err error) string {
	switch {
	case errors.Is(err, errProviderStreamCut):
		return providerRefusalStreamCut
	case errors.Is(err, elelem.ErrQuotaExhausted):
		return providerRefusalQuota
	case errors.Is(err, elelem.ErrContextExceeded):
		return providerRefusalContext
	}

	if status := providerHTTPStatus(err); status > 0 {
		return strings.TrimSpace(fmt.Sprintf(
			providerRefusalStatusFormat, status, http.StatusText(status)))
	}

	switch {
	case errors.Is(err, commerr.ErrNotAuthenticated):
		return providerRefusalAuth
	case errors.Is(err, commerr.ErrRateLimited):
		return providerRefusalRateLimit
	case errors.Is(err, commerr.ErrNotFound):
		return providerRefusalMissing
	default:
		return providerRefusalDefault
	}
}

// permanentModelFailure marks a model activity failure that another attempt
// cannot fix, so go-workflows does not run the activity again. A provider
// refusal becomes a providerRefusalError with a clean text. Every other
// failure, such as a store error, stays retryable.
func permanentModelFailure(err error) error {
	var tokenLimit *tokenLimitError

	switch {
	case errors.As(err, &tokenLimit):
		return ctxerrors.Wrap(workflow.NewPermanentError(err),
			"turn token limit")
	case isProviderRefusal(err):
		return ctxerrors.Wrap(
			workflow.NewPermanentError(newProviderRefusalError(err)),
			"provider refused the model request")
	default:
		return err
	}
}
