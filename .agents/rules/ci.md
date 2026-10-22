# CI workflows

`.github/workflows/` holds thin caller workflows. The real jobs live in `psyb0t/reusable-github-workflows`, referenced at `@master`. Third-party actions are pinned by full commit SHA inside that repository, and Dependabot updates them.

## pipeline.yml

Runs on pushes to `main`, `v*` tags, pull requests to `main`, a weekly schedule that refreshes the badges, and manual dispatch. Top-level `permissions: {}`, and each job asks for only what it needs.

A release tag is never rebuilt or retested. Its run waits for the `main` push run of the same commit, promotes the image that run built and tested, and publishes from it. So `git-update.sh` must push `main` before the tag, and a commit that never passed CI on `main` cannot be released.

| Job | When | What it runs |
| --- | --- | --- |
| `code` | every run except tags and draft pull requests | `make lint`, `make test-action test-coverage test-browser test-execution-forms test-installer`, `make sec`, and `make generate`. It uploads `coverage-percent.txt` and the `sec.sarif` report. |
| `docker` | `main` pushes and ready pull requests, after `code` | Multi-arch `linux/amd64,linux/arm64` image build and scan, published to Docker Hub as `psyb0t/peen:latest` and `psyb0t/peen:sha-<commit>`. The image is built with `BUILD_VERSION=sha-<commit>`, so it names `psyb0t/peen:sha-<commit>` as its default worker image. |
| `wait` | tags | Waits for the `main` push run of the tagged commit and fails when that run failed or never ran. Outputs its run ID. |
| `docker-promote` | tags, after `wait` | Re-tags `psyb0t/peen:sha-<commit>` as `psyb0t/peen:<tag>` by digest, for every platform, with no build, then scans it. It creates no GitHub release. |
| `badges` | `main` and the schedule after `code`; tags after `wait` | Coverage, version, license, and importer badges on the `badges` branch. A tag reads the coverage artifact from the `main` run `wait` found. |
| `release` | tags, after `docker-promote` | The GitHub release. Its body is the annotated tag's notes. |
| `publish-to-clawhub` | tags, after `docker-promote` | Publishes the `peen` skill from `.agents/skills/peen`. |

## What a change must not break

- The Make targets and output files the `code` job names. Renaming `lint`, `test-coverage`, `test-browser`, `sec`, or `generate`, or the `coverage-percent.txt` and `sec.sarif` paths, breaks CI and the coverage badge.
- Committed generated output. CI regenerates, so commit `make generate` output together with its source.
- The coverage floor of 80 percent.
- `badges` keeps `needs: [code, wait]`, because it reads the coverage artifact `code` uploads, from the `main` run on a tag.
- `docker` keeps `push_sha_tag: true` and `BUILD_VERSION=sha-<commit>`, because `docker-promote` re-tags exactly that image and the promoted image must still name a tag that holds it.
- Publishing stays tag-only, and the weekly schedule never publishes an image.
- Tests must pass on a 4-CPU runner under `-race`. A positive wait uses a generous named package constant, never a hand-picked short window, because the `main` run is the only test gate before a release.

## Other workflows

- `collaborators-only.yml` closes and locks pull requests from anyone who is not a collaborator. It runs on `pull_request_target` and never checks out or runs pull request code. Keep it that way.
- `mirror-and-archive.yml` mirrors every push to GitLab and Codeberg and saves the repository to the Wayback Machine. `issue-pull.yml` relays issues opened on the mirrors back to GitHub.

## Rules for workflow changes

- Keep callers short. If a job needs logic the reusable workflow lacks, the fix belongs in the reusable repository.
- Never combine `pull_request_target` with a checkout of pull request code.
- Grant the smallest `permissions:` a job needs.
- Secrets come only from repository secrets. Never echo them or pass them as Docker build arguments.
- `pipeline.yml` is listed in `.servicepackupdateignore`, so framework updates do not overwrite it.
