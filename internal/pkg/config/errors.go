package config

import "errors"

var (
	// ErrInvalidConfig reports invalid deployment configuration.
	ErrInvalidConfig = errors.New("invalid Peen configuration")
	// ErrInvalidUpstream reports malformed named LLM provider configuration.
	ErrInvalidUpstream = errors.New("invalid Peen upstream")
)
