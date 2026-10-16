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
//
// Pass the server's REST base, such as "http://127.0.0.1:8080/api/v1", to
// NewClient. Peen mounts REST under /api/v1 and the WebSocket separately
// under /ws/v1; this client only talks to the former.
package client

//go:generate go tool oapi-codegen --config=config.yaml ../../../../api/api.yml
