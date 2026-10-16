// Package asyncapirt is the support code that gen-asyncapi generated
// packages call: validation helpers, strict object decoding, incoming message
// matching, and the transport interface the generated client sends and
// receives through.
package asyncapirt

import "errors"

var (
	// ErrUnknownMessage means an incoming message matched none of the messages
	// the client receives.
	ErrUnknownMessage = errors.New("unknown message")
	// ErrAmbiguousMessage means an incoming message decoded as more than one of
	// the messages the client receives, and nothing in the spec or generator
	// config tells them apart.
	ErrAmbiguousMessage = errors.New("ambiguous message")
	// ErrNoVariantMatched means a union value matched none of its variants.
	ErrNoVariantMatched = errors.New("no union variant matched")
	// ErrAmbiguousVariant means a union value matched several variants of a
	// union without a discriminator.
	ErrAmbiguousVariant = errors.New("ambiguous union variant")
	// ErrUnknownDiscriminator means a union discriminator value names no
	// variant.
	ErrUnknownDiscriminator = errors.New("unknown discriminator value")
)
