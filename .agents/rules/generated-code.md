# Generated code

A generated file is never edited by hand. The next regeneration would erase the edit, or CI would catch the mismatch. Change the source, run the generator, and commit the source and the output in the same change.

Recognize generated output by a `Code generated ... DO NOT EDIT.` header, a `*.gen.go` name, or a path in the table below.

| Output | Source | Generator | Regenerate with |
| --- | --- | --- | --- |
| `internal/pkg/http/api/api.gen.go` | `api/api.yml`, `internal/pkg/http/api/config.yaml` | oapi-codegen, strict standard-library server | `make generate` |
| `pkg/http/api/client/client.gen.go` | `api/api.yml`, `pkg/http/api/client/config.yaml` | oapi-codegen client | `make generate` |
| `web/src/lib/api/generated.ts` | `api/api.yml` | openapi-typescript through `scripts/web/run.sh` | `make generate` or `make web-generate` |
| `internal/pkg/db/repositories/*.gen.go` | `internal/pkg/db/models/`, `cmd/repogen/` | gorm/gen through `cmd/repogen` | `make generate` |
| `.env.example` | `internal/pkg/config/envtemplate/env.tmpl` | `cmd/envexample`, run as `go tool envexample` from `internal/pkg/config/envtemplate/gen.go` | `make generate` |
| `internal/pkg/services/services.gen.go` | the service types under `internal/pkg/services/` | Servicepack's `service_registration.sh` | `make service-registration` |
| `internal/pkg/http/server/web/dist/` | `web/`, `api/api.yml` | Vite through `scripts/web/run.sh` | `make web-build` |
| `internal/pkg/http/wsapi/wsapi.gen.go` | `api/asyncapi.yaml`, `internal/pkg/http/wsapi/config.yaml` | gen-asyncapi, Go models and strict codecs, run as `go tool gen-asyncapi` | `make generate` |
| `web/src/lib/ws/generated.ts` | `api/asyncapi.yaml` | gen-asyncapi, TypeScript, run as `go tool gen-asyncapi` from `internal/pkg/http/server/webgen.go` | `make generate` |

`vendor/` is generated too, by `go mod vendor`. See `dependencies.md`.

## How generators are declared

`make generate` is `go generate ./...`. Each package that receives generated output holds a `gen.go` with only the package clause, an optional package doc comment, and `//go:generate` lines. Paths in those lines are relative to the `gen.go` directory, tool config sits next to it, and output lands in the same directory. `.env.example` is the one exception: the repository root has no Go package, so the template's package `internal/pkg/config/envtemplate` holds its `gen.go` and writes to the root. `internal/pkg/http/server/webgen.go` does the same for the web types.

- Run Go generators with `go tool <name>` from the `tool` block in `go.mod`. That includes Peen's own `cmd/repogen`, which is listed there and run as `go tool repogen`. Never `go run` a generator.
- A new generated package gets its own `gen.go`. Do not add a generator call to the Makefile.
- Running `make generate` twice gives the same result as running it once.

## When a generator cannot express something

Extend the source instead of patching the output. Add the operation or schema to the spec source (`api/api.yml` today, `cmd/apigen/` once the planned move lands, see `http-api.md`), add a querier method in `cmd/repogen/`, or write a hand-written file next to the generated one in the same package. Never edit the output and hope.
