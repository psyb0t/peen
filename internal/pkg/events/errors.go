package events

import "errors"

var (
	// ErrInvalidType reports a type that is not lowercase dotted segments.
	ErrInvalidType = errors.New("invalid event type")
	// ErrReservedType reports an outside caller using a Peen-owned prefix.
	ErrReservedType = errors.New("event type prefix is reserved")
	// ErrInvalidDelivery reports a delivery mode Peen does not implement.
	ErrInvalidDelivery = errors.New("invalid event delivery mode")
	// ErrInvalidOptions reports an unusable bus configuration.
	ErrInvalidOptions = errors.New("invalid event bus options")
)
