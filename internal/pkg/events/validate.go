package events

import (
	"strings"

	"github.com/psyb0t/ctxerrors"
)

const (
	maxTypeLength    = 128
	maxTypeSegments  = 8
	typeSeparator    = "."
	minSegmentLength = 1
)

// ValidateType checks the shape every event type must have: one to eight
// lowercase dotted segments of letters and digits, starting with a letter.
// Validation is done by hand rather than with a regular expression so the
// package needs no compiled package-level state.
func ValidateType(eventType Type) error {
	if eventType == "" || len(eventType) > maxTypeLength {
		return ctxerrors.Wrap(ErrInvalidType, "type length")
	}

	segments := strings.Split(eventType, typeSeparator)
	if len(segments) > maxTypeSegments {
		return ctxerrors.Wrap(ErrInvalidType, "too many segments")
	}

	for _, segment := range segments {
		if err := validateTypeSegment(segment); err != nil {
			return err
		}
	}

	return nil
}

func validateTypeSegment(segment string) error {
	if len(segment) < minSegmentLength {
		return ctxerrors.Wrap(ErrInvalidType, "empty segment")
	}

	for index, character := range segment {
		if isLowercaseLetter(character) {
			continue
		}

		if index > 0 && isDigit(character) {
			continue
		}

		return ctxerrors.Wrap(ErrInvalidType, "segment character")
	}

	return nil
}

func isLowercaseLetter(character rune) bool {
	return character >= 'a' && character <= 'z'
}

func isDigit(character rune) bool {
	return character >= '0' && character <= '9'
}

// IsReserved reports a type in one of Peen's own namespaces.
func IsReserved(eventType Type) bool {
	for _, prefix := range ReservedPrefixes {
		if strings.HasPrefix(eventType, prefix) {
			return true
		}
	}

	return false
}

// ValidateExternalType applies the outside-caller rules: a valid type that is
// not in a reserved namespace, so a webhook cannot forge a job or agent event.
func ValidateExternalType(eventType Type) error {
	if err := ValidateType(eventType); err != nil {
		return err
	}

	if IsReserved(eventType) {
		return ctxerrors.Wrap(ErrReservedType, eventType)
	}

	return nil
}

// ValidateDelivery checks the delivery mode, treating empty as the default.
func ValidateDelivery(delivery Delivery) (Delivery, error) {
	switch delivery {
	case "":
		return DeliveryQueue, nil
	case DeliveryQueue, DeliveryWake:
		return delivery, nil
	default:
		return "", ctxerrors.Wrap(ErrInvalidDelivery, delivery)
	}
}
