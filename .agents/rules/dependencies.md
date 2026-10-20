# Dependencies

## Go modules

- `vendor/` is committed and produced only by `go mod vendor`. Never edit anything under `vendor/`, `go.sum`, or the module cache by hand. A bug in a dependency is fixed upstream and picked up with a version bump.
- Change modules only through the Make targets, which run in the dev container:
  - `make pkg-add PKG=module@version` adds one exact version.
  - `make pkg-update PKG=module` moves one module to its latest version.
  - `make pkg-upgrade` moves every direct module.
  - `make pkg-remove PKG=module` drops one.
  - `make pkg-lock` re-tidies and re-vendors without changing versions.
- Each target that changes a version runs `scripts/check-go-age.sh`, which refuses a third-party release younger than seven days. Modules under `github.com/psyb0t/` skip only the age check, not pinning, vendoring, or scanning.
- Never run `go get`, `go mod tidy`, or `go install` on the host.
- Development tools live in the `tool (...)` block of `go.mod` and run as `go tool <name>`. Add one with `make pkg-add-tool MODULE=module@version TOOL=package`.
- `.golangci.yml` runs depguard with an allowlist of import paths. A new direct import from a module that is not listed fails `make lint`. Add the module there in the same change, and only after it has passed the checks above.
- Run `make sec` after a change. govulncheck findings are fixed by upgrading. If that is blocked, document the advisory, the blocker, and whether Peen reaches the vulnerable code.

## Preferred libraries

Peen already uses these. Reach for them before adding something new: Servicepack for lifecycle, aichteeteapee for HTTP, middleware, the error envelope, and WebSocket, ctxerrors for errors, ctxscope and slogging for logging, gonfiguration for configuration, commander for running processes, elelem for model providers, common-go for shared helpers, gorm and gorm/gen for SQLite access, testify and testcontainers-go for tests.

## Frontend packages

- pnpm only, never npm or yarn, through `make web-pkg-add WEB_PKG=name@version`, `web-pkg-update WEB_PKG=name`, `web-pkg-upgrade`, `web-pkg-remove WEB_PKG=name`, and `web-pkg-lock`.
- Versions are exact (`--save-exact`). `pnpm.minimumReleaseAge` in `web/package.json` holds new releases back for seven days.
- Installs run with `--ignore-scripts`. Only packages in `pnpm.onlyBuiltDependencies`, currently `esbuild`, may run build scripts. Adding to that list needs a reason in the change.
- Commit `package.json` and `pnpm-lock.yaml` together.

## Images

Base images in `Dockerfile` and `Dockerfile.dev` are pinned by digest. Bump them on purpose. See `docker.md`.
