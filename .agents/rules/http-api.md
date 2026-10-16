# HTTP and WebSocket API

Peen has two specs: OpenAPI for REST, mounted at `/api/v1`, and AsyncAPI for the WebSocket, at `/ws/v1`. Each generates its own Go and TypeScript types, and each has its own change procedure below.

The OpenAPI spec is generated from Go, never written by hand. `cmd/apigen` builds the spec, one file per domain, and writes the generated YAML. `psyb0t/oapi-codegen` generates the strict standard-library server in `internal/pkg/http/api/` and the public client in `pkg/http/api/client/`, `psyb0t/oapixconstgen` turns the spec's `x-constants:` into typed Go constants, and openapi-typescript generates the web client types. `internal/pkg/http/server/` implements the server on aichteeteapee.

Transition: today the spec is still the hand-written `api/api.yml`, generated with upstream oapi-codegen. Moving it to `cmd/apigen` is planned work. Until it lands, edit `api/api.yml`; do not add new hand-written spec files or a second spec source.

## Changing an endpoint

1. Edit the domain file in `cmd/apigen/` (until the move lands, `api/api.yml`): path, parameters, schemas, every response status, and the error responses. A value shared between the spec and Go code, such as a page size limit, goes in `x-constants:` through `AddConstant`, never as a literal in both places.
2. Run `make generate`. CI regenerates and fails on any diff, so commit the generated files with their source.
3. Implement or update the method on `*server.Server`. `var _ api.StrictServerInterface = (*Server)(nil)` in `server.go` keeps it honest.
4. Add or update handler tests in `internal/pkg/http/server/` and black-box coverage in `tests/api/`.
5. Update `docs/http-api.md`, the web client if it calls the endpoint, and the CHANGELOG.

Never edit `api.gen.go` or `client.gen.go`, and never add a route outside the spec. Keep handler files grouped by area: `sessions.go`, `jobs.go`, `agentruns.go`, `workspaces.go`.

## Handlers

- A handler returns the generated response object for each documented status, such as `api.ListMessages404JSONResponse`. Do not write to the `http.ResponseWriter` from a strict handler.
- Errors use the aichteeteapee envelope `{"code", "message"}` through the generated `api.Error` type. Use the `aichteeteapee.ErrorCode*` constants. A Peen-specific code goes in `errorcodes.go`, documented in `api/api.yml` and `docs/http-api.md`.
- Client-visible messages never include an error's source location or internal detail. `clientMessage` strips the ctxerrors location. Unexpected failures answer 500 with the generic message and log the cause.
- Pass the request context down. Get the logger with `ctxscope.GetLogger(ctx)`.

## Router and middleware

`router.go` builds an aichteeteapee `serbewr.Router`. Global middleware runs request ID, access logging without query strings, panic recovery, security headers, CORS, and metrics. The `/api/v1` API group adds bearer authentication, content negotiation, a body size limit, JSON body checks, and validation of every request against the embedded spec. The SPA catch-all comes last. Add middleware to the narrowest group that needs it, and keep authentication ahead of anything that reads the body.

Bearer tokens are compared with `subtle.ConstantTimeCompare`. A new credential check does the same.

## WebSocket

The live feed is `/ws/v1` on aichteeteapee's `wshub`, with the `peen.v1` subprotocol. The WebSocket owns live messages and turns. REST owns durable reads and control, and never starts a turn. Its contract is `api/asyncapi.yaml` (AsyncAPI 3.0.0), not `api/api.yml`.

### Changing a frame

1. Edit `api/asyncapi.yaml`: the frame's schema, its `data` payload, and the `ServerEvent` or `MessageSend` union it belongs to.
2. Run `make generate`. It regenerates the Go types in `internal/pkg/http/wsapi` and the TypeScript types in `web/src/lib/ws/generated.ts`. CI regenerates and fails on any diff, so commit the generated files with their source.
3. Update the emitter that builds the frame and, for an outgoing frame, check it against the contract test that renders every frame type and validates it with `wsapi`.
4. Add or update tests in `internal/pkg/http/server` and the frame-reading coverage in `tests/api`.
5. Update `docs/http-api.md`, `docs/events.md`, the web client if it reads the frame, and the CHANGELOG.

Never edit `wsapi.gen.go` or `generated.ts` by hand.

## Compatibility

The REST paths, schemas, status codes, error codes, WebSocket frames, and event types are public. Removing or renaming any of them, or tightening what a request accepts, is a breaking change. See `versioning.md`.
