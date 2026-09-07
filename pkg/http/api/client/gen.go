// Package client holds Peen's generated HTTP API client.
//
// It is generated from the same `api/api.yml` document the server is generated
// from, so a caller and the service cannot drift: a change to the contract
// regenerates both or neither. Use this rather than hand-rolling requests
// against the documented paths.
//
// For an in-process caller, `pkg/peen` is the supported entry point and needs
// no HTTP at all. This package is for talking to a Peen that runs somewhere
// else.
package client

//go:generate go tool oapi-codegen --config=config.yaml ../../../../api/api.yml
