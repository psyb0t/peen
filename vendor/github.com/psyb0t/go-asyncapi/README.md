# go-asyncapi

[![Go Reference](https://pkg.go.dev/badge/github.com/psyb0t/go-asyncapi.svg)](https://pkg.go.dev/github.com/psyb0t/go-asyncapi)
[![CI](https://github.com/psyb0t/go-asyncapi/actions/workflows/pipeline.yml/badge.svg?branch=main)](https://github.com/psyb0t/go-asyncapi/actions/workflows/pipeline.yml)
[![coverage](https://raw.githubusercontent.com/psyb0t/go-asyncapi/badges/coverage.svg)](https://github.com/psyb0t/go-asyncapi/actions/workflows/pipeline.yml)
[![version](https://raw.githubusercontent.com/psyb0t/go-asyncapi/badges/version.svg)](https://github.com/psyb0t/go-asyncapi/tags)
[![license](https://raw.githubusercontent.com/psyb0t/go-asyncapi/badges/license.svg)](LICENSE)
[![imported by](https://raw.githubusercontent.com/psyb0t/go-asyncapi/badges/importers.svg)](https://github.com/psyb0t/go-asyncapi/blob/badges/importers.md)

Loads AsyncAPI 3 documents in Go. Point it at a YAML or JSON file and you get servers, channels, operations and messages as plain structs, with every `$ref` followed, across files too, and every trait merged in. [gen-asyncapi](https://github.com/psyb0t/gen-asyncapi) is built on it.

## Install

```sh
go get github.com/psyb0t/go-asyncapi
```

## Usage

```go
document, err := asyncapi.Load("asyncapi.yaml")
if err != nil {
	return err
}

for _, server := range document.Servers {
	fmt.Println(server.Name, server.URL()) // production wss://eu.example.com:443/v2/ws
}

for _, operation := range document.Operations {
	for _, message := range operation.Messages {
		fmt.Println(operation.Action, operation.Channel.Address, message.Name)
	}
}
```

Everything comes back in the order the document lists it. A message used by several channels is one `*Message`, so you can compare pointers. Traits are already merged: a trait fills what the message or operation leaves out and never overrides what it sets.

## Schemas

Payload and header schemas stay as they are in the document, as `*asyncapi.Object` trees that keep key order. `Object.Get`, `Object.String` and `Object.Object` read them, and the document's `Resolver` follows the `$ref`s inside:

```go
message := document.Messages[0]

schema, at, err := document.Resolver.Deref(message.Payload, message.PayloadAt)
if err != nil {
	return err
}

properties := schema.(*asyncapi.Object).Object("properties")
for _, name := range properties.Keys {
	fmt.Println(name)
}
```

`Deref` returns the resolved value and its `Location`, a file plus a JSON pointer. Two `$ref`s that land on the same `Location` point at the same schema, so it works as a map key when you need each schema once.

Payloads in AsyncAPI, JSON Schema or OpenAPI format load as schemas. Any other `schemaFormat`, such as Avro, leaves `Payload` nil and names the format in `UnsupportedFormat`.

## What it reads

- `servers` with variables, enums and defaults, plus `URL()` and `URLTemplate()`
- `channels` with addresses, parameters and messages; a null address stays empty
- `operations` with action, channel, messages and replies, including dynamic reply addresses
- `messages` with payload, headers, `correlationId`, content type (falling back to `defaultContentType`), bindings and the deprecated flag
- `components`, with every `$ref` into them resolved, and `components.schemas` listed in order
- local and relative-file `$ref`s, chains included; a cycle or a remote URL is an error

Errors wrap [`ctxerrors/commerr`](https://github.com/psyb0t/ctxerrors) sentinels, so `errors.Is(err, commerr.ErrNotFound)` catches a dangling `$ref` and `commerr.ErrInvalidValue` catches a malformed section.

## Development

Everything runs in the `Dockerfile.dev` image. You need Docker and Make.

```sh
make help           # list targets
make lint           # shellcheck, shfmt, go fix, golangci-lint
make test-coverage  # go test -race with a 90% coverage gate
make sec            # gitleaks, govulncheck, semgrep
```

## License

[MIT](LICENSE).
