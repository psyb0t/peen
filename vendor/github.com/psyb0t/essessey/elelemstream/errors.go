package elelemstream

import "errors"

var (
	// ErrRoundStreamNotInitialized means a delta or assistant message arrived
	// before OnRoundStart opened a round. It signals a callback wiring bug —
	// Bind registers OnRoundStart, so reaching this means the request was
	// built without it.
	ErrRoundStreamNotInitialized = errors.New("round stream is not initialized")

	// ErrToolResultMissing means OnToolResult fired with no result attached.
	// The block cannot be emitted without one, and inventing an empty result
	// would render an empty card as though the tool had genuinely returned
	// nothing.
	ErrToolResultMissing = errors.New("tool result is missing")
)
