# Instructions for agents working on Peen

Peen is a durable coding-agent service written in Go. One controller owns SQLite, the REST and WebSocket API, and the embedded browser control surface, and it runs each session's turns in its own worker process or container. `CLAUDE.md` is a link to this file and `.claude` is a link to `.agents`, so every agent reads the same instructions.

Start with [docs/development.md](docs/development.md) for the repository layout, the Make targets, and what "done" means. The topic rules in `.agents/rules/` hold the details, and a rule wins over this file and the guide when they disagree.

| Rule | Read it before you touch |
| --- | --- |
| [make.md](.agents/rules/make.md) | the Makefile, or any command you are about to run |
| [go.md](.agents/rules/go.md) | Go code: lint, errors, logging |
| [go-tests.md](.agents/rules/go-tests.md) | Go tests and the test tiers |
| [constants.md](.agents/rules/constants.md) | named values, enums, sentinels |
| [naming.md](.agents/rules/naming.md) | any name Peen invents |
| [config.md](.agents/rules/config.md) | `PEEN_` settings |
| [security.md](.agents/rules/security.md) | secrets, directories, workers, hooks, untrusted input |
| [http-api.md](.agents/rules/http-api.md) | REST endpoints and WebSocket frames |
| [database.md](.agents/rules/database.md) | the schema, migrations, queries |
| [generated-code.md](.agents/rules/generated-code.md) | anything generated, and how to regenerate it |
| [dependencies.md](.agents/rules/dependencies.md) | Go modules, pnpm packages, base images |
| [docker.md](.agents/rules/docker.md) | `Dockerfile`, `Dockerfile.dev`, the entrypoint |
| [shell.md](.agents/rules/shell.md) | shell scripts |
| [web.md](.agents/rules/web.md) | the control surface under `web/` |
| [ci.md](.agents/rules/ci.md) | GitHub workflows |
| [docs-and-prose.md](.agents/rules/docs-and-prose.md) | docs, README, and any prose |
| [versioning.md](.agents/rules/versioning.md) | the CHANGELOG, version numbers, commit messages |

The rules most often broken:

- Make is the only entry point, and every Go, Node, and lint command runs in the dev image. Never install or run Go, pnpm, or linters on the host.
- Run one lint, test, or build target at a time. Each one is a full container build or Go compile.
- Never edit generated files or `vendor/`. Change the source, run `make generate` or the `make pkg-*` targets, and commit both.
- A change that alters behavior, configuration, the API, events, or harness formats updates the docs, `.env.example`, and the CHANGELOG in the same change.
- Never commit a secret, and never write a provider key, `PEEN_API_TOKEN`, or a worker credential into config, logs, SQLite, events, or errors.
- A change is done when `make lint` is clean and the test tier that can see it passes, `make test-coverage` before release.
- Never add `Closes #N` or another issue-closing keyword to a commit unless the maintainer asked for that issue to be closed.

`internal/pkg/harness/builtin/AGENTS.md` is product text, the operating rules Peen ships to every user. Edit it as content. It is not an instruction for that directory.

`.agents/skills/peen` is the published skill for operating Peen, not a rule for changing it.
