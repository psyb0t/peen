# Instructions for agents working on Peen

Peen is a durable coding-agent service written in Go. One controller owns SQLite, the REST and WebSocket API, and the embedded browser control surface, and it runs each session's turns in its own worker process or container. `CLAUDE.md` is a link to this file and `.claude` is a link to `.agents`, so every agent reads the same instructions.

Start with [docs/development.md](docs/development.md) for the repository layout, the Make targets, and what "done" means. [.agents/rules/core.md](.agents/rules/core.md) holds the rules for every change.

Area rules live in `.agents/rule-details/`. Read the matching file in full before you edit that area or run its commands. An area rule wins over this file and over the guide when they disagree. [.agents/rule-gate.json](.agents/rule-gate.json) maps the same paths for tools that enforce the reads.

| Rule | Read it before you touch |
| --- | --- |
| [make.md](.agents/rule-details/make.md) | `Makefile`, `scripts/make/`, a new Make target |
| [go.md](.agents/rule-details/go.md) | any `.go` file: lint, errors, logging |
| [go-tests.md](.agents/rule-details/go-tests.md) | `*_test.go`, `tests/`, the test tiers |
| [constants.md](.agents/rule-details/constants.md) | `constants.go`, `errors.go`, `types.go`, `web/src/lib/common/constants.ts`, any named value or enum |
| [naming.md](.agents/rule-details/naming.md) | a new tool, hook event, event type, JSON or YAML key, or any other name Peen invents |
| [config.md](.agents/rule-details/config.md) | `internal/pkg/config/`, `.env.example`, a `PEEN_` setting |
| [security.md](.agents/rule-details/security.md) | secrets, directories, workers, `internal/pkg/worker/`, `internal/pkg/control/`, the outside-input packages `internal/pkg/telegram*/`, `mcpserver/`, `mcptools/`, `openaicompat/`, and `cron*/`, hooks, untrusted input |
| [http-api.md](.agents/rule-details/http-api.md) | `api/`, `internal/pkg/http/`, `pkg/http/`, REST endpoints, WebSocket frames |
| [database.md](.agents/rule-details/database.md) | `internal/pkg/db/`, `cmd/repogen/`, the schema, migrations, queries |
| [generated-code.md](.agents/rule-details/generated-code.md) | `gen.go`, `*.gen.go`, `generated.ts`, `.env.example`, `internal/pkg/http/server/web/dist/`, any generator or generated output |
| [dependencies.md](.agents/rule-details/dependencies.md) | `go.mod`, `go.sum`, `vendor/`, `web/` and `website/` `package.json` and pnpm files, `.golangci.yml` depguard, base images |
| [docker.md](.agents/rule-details/docker.md) | `Dockerfile`, `Dockerfile.dev`, `docker/`, `.dockerignore` |
| [shell.md](.agents/rule-details/shell.md) | `scripts/`, `install.sh`, any shell script |
| [web.md](.agents/rule-details/web.md) | `web/`, `internal/pkg/http/server/web/` |
| [ci.md](.agents/rule-details/ci.md) | `.github/`, `action.yml` |
| [docs-and-prose.md](.agents/rule-details/docs-and-prose.md) | `docs/`, `website/`, `scripts/make/docs*`, `README.md`, `AGENTS.md`, `.agents/`, any prose |
| [model-facing-text.md](.agents/rule-details/model-facing-text.md) | the system prompt, `internal/pkg/harness/builtin/`, tool descriptions, injected blocks, anything a model reads |
| [versioning.md](.agents/rule-details/versioning.md) | `CHANGELOG.md`, version numbers, `.agents/.codex-plugin/plugin.json`, commit messages |
