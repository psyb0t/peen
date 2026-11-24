# Rules for every change

These hold for every change. Area rules live in `.agents/rule-details/`, and the table in `AGENTS.md` says which file to read before touching which part of the repository. Read the matching file before you edit that area or run its commands; it wins over this file and over `docs/development.md` when they disagree.

## Commands

- Make is the only entry point. Every Go, Node, and lint command runs in the dev image through a Make target. Never install or run Go, pnpm, or linters on the host.
- Run one lint, test, or build target at a time, never in parallel from one shell or from several agents. Each one is a full container build or Go compile.

## Generated code and dependencies

- Never edit generated files (`*.gen.go`, `generated.ts`, `.env.example`, the embedded web build) or `vendor/`. Change the source, run `make generate`, the `make pkg-*` targets, or `make web-build`, and commit the source and the output together.
- Change Go modules and pnpm packages only through their Make targets.

## Done

- A change is done when `make lint` is clean and the test tier that can see it passes. Run `make test-coverage` before a release.
- A change that alters behavior, configuration, the API, events, harness formats, or deployment updates the docs, `.env.example`, and the CHANGELOG in the same change.
- Every name Peen invents and owns is camelCase. Read `.agents/rule-details/naming.md` before adding one.

## Security floor

- Never commit a secret. Never write a provider key, `PEEN_API_TOKEN`, or a worker credential into config, logs, SQLite, events, errors, or API responses.
- Never weaken an isolation boundary: Docker workers get no state directory, no API token, and no Docker socket unless their profile grants it; workspaces stay inside `PEEN_WORKSPACE_ROOT`; no client request chooses an image, mount, network, or capability; processes start from an argument list, and only `runCommand` runs a shell. Read `.agents/rule-details/security.md` before touching secrets, directories, workers, hooks, or untrusted input.

## Commits and committed text

- Never add `Closes #N` or another issue-closing keyword unless the maintainer asked for that issue to be closed.
- Committed text never mentions a contributor's local paths, scratch directories, private tools, or session markers.
- `internal/pkg/harness/builtin/AGENTS.md` is product text that Peen ships to every user, not an instruction for that directory. `.agents/skills/peen` is the published skill for operating Peen, not a rule for changing it.
