# The benchmark driver: Harbor plus the Docker CLI it uses to start each task
# container on the host daemon. `make bench-image` builds it, and the bench
# targets run it as the caller's user with the Docker socket, which is the
# development-only exception those targets document. It never runs a task
# itself and never holds a provider key.
FROM docker:29.9.1-cli@sha256:6fae2214c3121a28d5aa639d09032f61310b7c9c75ffbad1025f261a1b621800 AS docker

FROM python:3.12.15-slim@sha256:a0f57064cbdcd6285fa45f51237ab0b35ea8ebaed06a1f5973e89e19dfb86704

RUN apt-get update && \
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		ca-certificates git && \
	rm -rf /var/lib/apt/lists/*

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --require-hashes -r /tmp/requirements.txt && \
	rm /tmp/requirements.txt

COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx

ENV HARBOR_DISABLE_TELEMETRY=1 HOME=/tmp

USER 10001:10001
