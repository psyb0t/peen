package events

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

// Limits are the size bounds the bus applies to one event.
type Limits struct {
	// MaxSummaryBytes bounds the one-line summary a model reads.
	MaxSummaryBytes int
	// MaxDataBytes bounds the structured payload.
	MaxDataBytes int
}

// DefaultLimits returns the bounds a bus uses when none are configured.
func DefaultLimits() Limits {
	return Limits{
		MaxSummaryBytes: defaultMaxSummaryBytes,
		MaxDataBytes:    defaultMaxDataBytes,
	}
}

// LimitedPublisher is a Publisher that can report the bounds it enforces, so a
// caller with untrusted content can refuse an oversized event instead of
// having the bus cut it silently.
type LimitedPublisher interface {
	Publisher
	EventLimits() Limits
}

// Validate rejects a summary or data payload over the bounds. The error
// matches both ErrEventTooLarge and commerr.ErrValidationFailed, so an HTTP
// boundary maps it to a client error.
func (l Limits) Validate(summary string, data json.RawMessage) error {
	if len(summary) > l.MaxSummaryBytes {
		return ctxerrors.Wrapf(
			errors.Join(ErrEventTooLarge, commerr.ErrValidationFailed),
			"event summary is %d bytes, the limit is %d",
			len(summary),
			l.MaxSummaryBytes,
		)
	}

	if len(data) > l.MaxDataBytes {
		return ctxerrors.Wrapf(
			errors.Join(ErrEventTooLarge, commerr.ErrValidationFailed),
			"event data is %d bytes, the limit is %d",
			len(data),
			l.MaxDataBytes,
		)
	}

	return nil
}

// truncateToRuneBoundary cuts text to at most maxBytes without leaving half of
// a multi-byte character at the end.
func truncateToRuneBoundary(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}

	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}

	return text[:cut]
}
