# The benchmark driver: Harbor plus the Docker CLI it uses to start each task
# container on the host daemon. `make bench-image` builds it, and the bench
# targets run it as the caller's user with the Docker socket, which is the
# development-only exception those targets document. It never runs a task
# itself and never holds a provider key.
FROM docker:29.9.1-cli@sha256:6fae2214c3121a28d5aa639d09032f61310b7c9c75ffbad1025f261a1b621800 AS docker

FROM python:3.12.15-slim@sha256:a0f57064cbdcd6285fa45f51237ab0b35ea8ebaed06a1f5973e89e19dfb86704

# The build context is bench/, so the shared docker/build-retry.sh is out of
# reach. Each fetch retries the same way inline: 5 attempts, 15 seconds before
# the second, doubling. --error-on=any makes a failed index download fail
# apt-get update instead of only warning.
RUN attempt=1 delay=15; \
	until apt-get -o Acquire::Retries=5 update --error-on=any && \
		DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
			ca-certificates git; do \
		if [ "$attempt" -ge 5 ]; then exit 1; fi; \
		echo "apt failed on attempt $attempt, retrying in ${delay}s" >&2; \
		sleep "$delay"; delay=$((delay * 2)); attempt=$((attempt + 1)); \
	done && \
	rm -rf /var/lib/apt/lists/*

# requirements.txt is the complete hashed lock, overrides included, so pip
# installs exactly it and resolves nothing. Its resolver would refuse the
# filelock override in bench/overrides.txt.
COPY requirements.txt /tmp/requirements.txt
RUN attempt=1 delay=15; \
	until pip install --no-cache-dir --require-hashes --no-deps -r /tmp/requirements.txt; do \
		if [ "$attempt" -ge 5 ]; then exit 1; fi; \
		echo "pip failed on attempt $attempt, retrying in ${delay}s" >&2; \
		sleep "$delay"; delay=$((delay * 2)); attempt=$((attempt + 1)); \
	done && \
	rm /tmp/requirements.txt

COPY --from=docker /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose
COPY --from=docker /usr/local/libexec/docker/cli-plugins/docker-buildx /usr/local/libexec/docker/cli-plugins/docker-buildx

# LITELLM_LOCAL_MODEL_COST_MAP keeps litellm on its packaged model map instead
# of fetching one from its GitHub main branch on import.
ENV HARBOR_DISABLE_TELEMETRY=1 HOME=/tmp LITELLM_LOCAL_MODEL_COST_MAP=True

USER 10001:10001
