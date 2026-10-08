// Package api embeds Peen's OpenAPI document, the single source for the
// generated server, the Go client, and the web client's types.
package api

import _ "embed"

// Spec is api.yml exactly as written. The server validates requests against
// it. It is embedded as text rather than as the generator's compressed blob,
// so secret scanning reads the same document reviewers do.
//
//go:embed api.yml
var Spec []byte
