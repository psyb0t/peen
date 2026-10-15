# CI workflows

`.github/workflows/` holds thin caller workflows. The real jobs live in `psyb0t/reusable-github-workflows`, referenced at `@master`. Third-party actions are pinned by full commit SHA inside that repository, and Dependabot updates them.

## pipeline.yml

Runs on pushes to `main`, `v*` tags, pull requests to `main`, a weekly schedule that refreshes the badges, and manual dispatch. Top-level `permissions: {}`, and each job asks for only what it needs.

| Job | When | What it runs |
| --- | --- | --- |
| `code` | every run except draft pull requests | `make lint`, `make test-coverage test-browser test-execution-forms test-installer`, `make sec`, and `make generate`. It uploads `coverage-percent.txt` and the `sec.sarif` report. |
| `docker` | pushes and ready pull requests, after `code` | Multi-arch `linux/amd64,linux/arm64` image build and scan, published to Docker Hub as `psyb0t/peen`. |
| `badges` | the default branch and tags, after `code` | Coverage, version, license, and importer badges on the `badges` branch. |
| `release` | tags, after `code` | The GitHub release. |
| `publish-to-clawhub` | tags, after `docker` | Publishes the `peen` skill from `.agents/skills/peen`. |

## What a change must not break

- The Make targets and output files the `code` job names. Renaming `lint`, `test-coverage`, `test-browser`, `sec`, or `generate`, or the `coverage-percent.txt` and `sec.sarif` paths, breaks CI and the coverage badge.
- Committed generated output. CI regenerates, so commit `make generate` output together with its source.
- The coverage floor of 80 percent.
- `badges` keeps `needs: [code]`, because it reads the coverage artifact that job uploads.
- Publishing stays tag-only, and the weekly schedule never publishes an image.

## Other workflows

- `collaborators-only.yml` closes and locks pull requests from anyone who is not a collaborator. It runs on `pull_request_target` and never checks out or runs pull request code. Keep it that way.
- `mirror-and-archive.yml` mirrors every push to GitLab and Codeberg and saves the repository to the Wayback Machine. `issue-pull.yml` relays issues opened on the mirrors back to GitHub.

## Rules for workflow changes

- Keep callers short. If a job needs logic the reusable workflow lacks, the fix belongs in the reusable repository.
- Never combine `pull_request_target` with a checkout of pull request code.
- Grant the smallest `permissions:` a job needs.
- Secrets come only from repository secrets. Never echo them or pass them as Docker build arguments.
- `pipeline.yml` is listed in `.servicepackupdateignore`, so framework updates do not overwrite it.
