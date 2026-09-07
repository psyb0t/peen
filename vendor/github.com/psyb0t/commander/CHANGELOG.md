# Changelog

All notable changes per release. Versions follow [semver](https://semver.org).

## v0.5.8 — 2026-08-08

Repository infrastructure only. No library code changed.

- Added the imported-by badge: a count of the public packages importing this
  module, linking to `importers.md` on the `badges` branch — the importing
  repositories, grouped, package counts descending, and flagged when the owner
  differs from this repo's.
- It measures **blast radius, not adoption**. The number tells you how much
  breaks when an exported name moves; the external mark tells you whether any of
  that is someone else's problem, which is what decides how strictly the module
  has to be versioned. Stars answer neither — nobody stars an `os/exec` wrapper,
  they just import it.
- Refreshed weekly rather than daily, because pkg.go.dev's crawl lags
  publication by days and each run drags the full test suite along (the badges
  job needs the coverage artifact). The whole pipeline runs rather than a
  badges-only job: the badge publisher republishes only what a run produced, so
  a badge-only refresh would delete the coverage, version and license badges.
- The cron slot is derived from a hash of the repository name rather than
  chosen — GitHub cron has no randomness, and its scheduler sheds queued runs
  hardest at the round times a human would pick.

## v0.5.7 — 2026-08-01

Infrastructure only — no library code changed.

- Every branch and tag push is now mirrored to GitLab and Codeberg.
- The default branch and tags are archived to the Wayback Machine and Software Heritage, on push and monthly.
- Issues opened on the mirrors are pulled back into GitHub every six hours.

## v0.5.6 — 2026-07-27

- Added a GitHub Actions CI status badge to the README.

## v0.5.5 — 2026-07-27

Fix badges CI job — add needs dependency so the coverage badge waits for the coverage artifact.

## v0.5.4 — 2026-07-27

Modernize the toolchain and CI.

- Go 1.26.
- `make lint` now runs `go fix -diff ./...` before `golangci-lint` (was `go tool modernize`, which is dropped along with its dependency).
- Added a coverage badge — `test-coverage` writes `coverage-percent.txt`, wired into the `badges` CI job and README.
- Logging (`log/slog`) and error wrapping (`github.com/psyb0t/ctxerrors`) were already in place; verified, no changes needed.

## v0.5.3 — 2026-07-27

Add README status badges.

- Added self-hosted version and license badges (rendered as SVGs on the `badges` branch by the `create-badges` CI job, no third-party render service). Wired a badges job into pipeline.yml.
