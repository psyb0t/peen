# Docker images

| File | Owner | Role |
| --- | --- | --- |
| `Dockerfile` | Peen | The published `psyb0t/peen` image. One image, two roles: the controller by default (`run`) and every Docker worker (`worker`). |
| `Dockerfile.dev` | Peen | The dev toolchain image every Make target runs in: Go, Node, pnpm, shellcheck, shfmt, semgrep, docker-cli. |
| `docker/entrypoint.sh` | Peen | Entrypoint for both roles. |
| `Dockerfile.servicepack`, `Dockerfile.servicepack.dev` | Servicepack | Framework fallbacks. Unused while Peen's own files exist. Not edited here. |
| `.dockerignore` | Peen | Keeps secrets, scratch, docs, and `.agents` out of the build context. |

## Production image

- Three stages: Node builds the web client, Go builds a static binary with `CGO_ENABLED=0` and the embedded web build, and Ubuntu is the runtime. The runtime is a full Ubuntu on purpose, because `run_command` gives the agent a shell and the image is its toolbox. Do not switch it to distroless or scratch.
- The runtime holds only the binary, the entrypoint, and the tools the agent needs. No compiler, source tree, or package cache. Clean apt lists in the same `RUN`.
- The controller runs as the fixed non-root `appuser` (UID and GID 10001). `COPY` sets ownership explicitly.
- `tini` is PID 1, in exec form, so signals reach Peen and `run_command` children are reaped.
- There is no `HEALTHCHECK`, because a worker container from the same image has no HTTP listener. Readiness belongs to whatever starts the controller. `docs/deployment.md` documents the probe.
- Set file modes with `RUN chmod`, not `COPY --chmod`. testcontainers builds this image with the classic builder, which does not support `--chmod`.
- Build arguments are `BUILD_COMMIT`, `BUILD_VERSION`, and the coverage switch. Never pass a secret as an `ARG`, an `ENV`, or a copied file.

## Pinning

- Every `FROM` names a readable tag and a digest: `golang:1.26.9-alpine@sha256:...`. Bump them on purpose, never to `latest`.
- The Go version matches in `go.mod`, `Dockerfile`, and `Dockerfile.dev`. The Servicepack Dockerfiles follow through framework updates. The Node version matches between `Dockerfile` and `Dockerfile.dev`. A Go bump is a CHANGELOG entry.

## Entrypoint

`docker/entrypoint.sh` does nothing for the controller. For a worker it starts as root, creates or reconciles the controller's host account from operator configuration, grants `sudo` only when the execution profile sets `allowPrivilegeEscalation`, and drops to that account with `setpriv` before Peen runs. The agent never runs as root. Every input comes from the controller, never from a client request or model output. It does not tee to a log file, because the worker reads its launch document from stdin.

## Build context

Review `.dockerignore` when you add a build input or a new local scratch directory. Do not exclude `vendor/`, test files, or Markdown inside the source tree, which a package may embed.

## Checks

- `make docker-build` builds the production image.
- `make test-docker-worker-source` builds this checkout's image and proves the worker drops to the host account with no sudo, no Docker socket, no network, and no controller state. Run it after any change to `Dockerfile` or `docker/entrypoint.sh`.
- `make test-api` boots the real image through `tests/testinfra`.
