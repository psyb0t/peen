# Changelog

All notable changes per release. Versions follow [semver](https://semver.org).

## v1.1.6 — 2026-08-01

Repository infrastructure only, no tool change.

- Every push now mirrors the repo to GitLab and Codeberg, so the source stays
  fetchable if GitHub is unavailable. Gitee is wired but left off — it binds repo
  creation to a mobile number and silently creates the repo private without one.
- Pushes to the default branch and every tag are archived to the Wayback Machine
  and Software Heritage, through the authenticated Save Page Now API, with README
  outlinks captured too. Feature-branch pushes are skipped because the archive is
  rate-limited.
- Issues filed on the Codeberg and GitLab mirrors are pulled back into GitHub
  every six hours, so a bug reported on a mirror reaches the same tracker.
  Scheduled runs jitter to avoid stampeding the mirrors; a manual run does not.
- Pull requests are auto-closed and locked with a pointer to the issue tracker
  (this landed earlier but was never written down).
- Note on tags: `v1.1.1` through `v1.1.5` were released as commits and changelog
  entries but never actually tagged, so `v1.1.6` is the first tag since `v1.1.0`
  and carries all of that work — the LICENSE file, the Go 1.26 bump, the
  `ctxerrors` wrapping and the badges — along with the CI changes above.

## v1.1.5 — 2026-07-27

- Added a GitHub Actions CI status badge to the README.

## v1.1.4 — 2026-07-27

- Fix badges CI job — add needs dependency so the coverage badge waits for the
  coverage artifact.

## v1.1.3 — 2026-07-27

- Go 1.26; `make lint` now runs `go fix -diff` before `golangci-lint` (drops the
  `modernize` tool in favor of the standard library's own fixer).
- Added a coverage badge; `make test-coverage` now writes `coverage-percent.txt`
  and the pipeline's `badges` job publishes it.
- Errors returned across function boundaries wrap with `ctxerrors.Wrap` for
  file/line/function capture instead of `fmt.Errorf`.
- Logging already used `log/slog` directly (this is a CLI tool, not a service —
  no global logging configuration was added).

## v1.1.2 — 2026-07-27

- Added self-hosted version and license badges; wired a badges job into pipeline.yml.

## v1.1.1 — 2026-07-27

Add a LICENSE file.

- Add `LICENSE` (MIT). The project previously shipped without a license file; the
  tool's source is now explicitly MIT-licensed. Vendored dependencies under
  `vendor/` retain their own upstream licenses.

## v1.1.0 — 2026-03-29

- Add a `-file` flag for a custom output file name.

## v1.0.0 — 2026-01-28

Initial release. Extracts typed constants from an OpenAPI spec's `x-constants`
extension and generates a Go file with properly typed constants.
