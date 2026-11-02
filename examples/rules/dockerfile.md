---
paths:
  - "**/Dockerfile"
  - "**/Dockerfile.*"
  - "**/*.dockerfile"
  - "**/Containerfile"
---

# Dockerfiles

Sources: Docker's [building best practices](https://docs.docker.com/build/building/best-practices/) and [hadolint](https://github.com/hadolint/hadolint).

Follow the project's existing Dockerfiles and build scripts first. Use this rule where the project says nothing.

- Pin every base image to a specific version tag, and to a digest when the project does. Never use `latest`.
- Use a multi-stage build when the image compiles something, so the final stage holds only what runs.
- Run the final stage as a non-root `USER`.
- Run `apt-get update` and `apt-get install --no-install-recommends` in the same `RUN`, and remove `/var/lib/apt/lists/*` in that `RUN`.
- Use `COPY`, not `ADD`, unless you need `ADD` to unpack a local archive.
- Copy dependency manifests and install dependencies before copying the source, so a source change does not rebuild the dependency layer.
- Write `CMD` and `ENTRYPOINT` in exec form: `CMD ["app", "--flag"]`.
- Never put a secret in `ARG`, `ENV`, or a copied file. Use build secrets.
- Keep `.dockerignore` excluding version control, local environment files, and build output.
- Run `hadolint` on the Dockerfile when it is available.
