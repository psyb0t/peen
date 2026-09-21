# Production Dockerfile - Multi-stage build
FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS builder

ARG BUILD_COMMIT=""
ARG BUILD_VERSION="dev"
ARG PEEN_ENABLE_COVERAGE="false"

# Install build dependencies
RUN apk add --no-cache \
    gcc \
    musl-dev

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build binary with static linking.
#
# The app name is derived from go.mod's module path and injected into
# main.appName, which cmd/main.go uses as cobra's Use:/Short:. cmd/init.go
# already replaces the framework's default name, so this only keeps the built
# image and a from-source build reporting the same name through the same path.
RUN APP_NAME="$(head -n 1 go.mod | awk '{print $2}' | awk -F'/' '{print $NF}')" && \
	if [ "$PEEN_ENABLE_COVERAGE" = "true" ]; then \
		CGO_ENABLED=0 go build -a -cover \
		-ldflags "-X main.appName=${APP_NAME} -X main.buildCommit=${BUILD_COMMIT} -X main.buildVersion=${BUILD_VERSION}" \
		-o ./build/app ./cmd; \
	else \
		CGO_ENABLED=0 go build -a \
		-ldflags "-X main.appName=${APP_NAME} -X main.buildCommit=${BUILD_COMMIT} -X main.buildVersion=${BUILD_VERSION}" \
		-o ./build/app ./cmd; \
	fi

# Final stage - Ubuntu runtime
#
# A static Go binary would normally ship on scratch or distroless. Peen does
# not, on purpose: `run_command` executes arbitrary shell commands as the
# product's core feature, so the runtime image IS the agent's toolbox. A
# distroless image would leave the agent with no shell and no utilities, and
# Ubuntu is the distribution whose layout and tooling agents handle most
# reliably. The image is correspondingly larger. That is the trade.
#
# Pinned by digest, not by tag: a tag is mutable, so `ubuntu:24.04` lets an
# upstream republish different bytes under the same name and your next build
# silently picks them up. Bump this deliberately.
FROM ubuntu:24.04@sha256:33ceb71981b602c1a7443a53469e4dba065f7503eab3078a2d7a57a2ab987517

# Runtime tooling the agent's shell is expected to have. ca-certificates is
# required for outbound HTTPS; the rest are the utilities an agent reaches for
# first. Trim this list if a deployment wants a smaller blast radius.
#
# sudo is installed but authorizes nobody. The image ships no sudoers rule, so
# it stays inert until a worker container that the controller started under an
# execution profile marked allowPrivilegeEscalation writes one for that worker's
# own account. util-linux supplies setpriv, which is how that bootstrap drops
# back to a non-root account before the agent runs.
RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        ripgrep \
        sudo \
        tini \
        tzdata \
        unzip \
        util-linux && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Create a fixed non-root UID/GID so bind-mounted host state has predictable
# ownership across rebuilds.
RUN groupadd --gid 10001 appuser && \
    useradd --uid 10001 --gid 10001 --create-home --shell /bin/bash appuser

# Set working directory
WORKDIR /app

# Copy binary from builder stage with explicit ownership
COPY --from=builder --chown=appuser:appuser /app/build/app .

# The entrypoint is a no-op for the non-root control plane. Every Docker worker
# starts as root only long enough to reconcile the controller host account and
# drop to it. A profile that permits escalation additionally grants that account
# sudo. See docker/entrypoint.sh.
#
# The mode is set by a RUN rather than COPY --chmod, because --chmod needs
# BuildKit and this image is also built by the classic builder that
# testcontainers drives.
COPY --chown=root:root docker/entrypoint.sh /usr/local/bin/peen-entrypoint
RUN chmod 0755 /usr/local/bin/peen-entrypoint

# Switch to non-root user
USER appuser

# tini reaps the processes run_command spawns and forwards signals to the app.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/peen-entrypoint", "./app"]

# The image is one binary with two roles, and its default is the control plane:
# `docker run psyb0t/peen` starts a controller.
#
# The same image is also the worker image. The controller creates a container
# from it with the `worker` command and hands that worker its launch document on
# stdin, so a worker is this same ENTRYPOINT under a different command, never a
# second control plane.
#
# A controller in this image always starts native workers inside itself. It can
# only start sibling worker containers when a Docker socket is mounted; a Docker
# execution profile without one is refused outright rather than run natively.
CMD ["run"]

# No HEALTHCHECK on purpose. Only the control-plane role listens on HTTP; a
# worker container built from this same image serves the private controller
# socket and would report permanently unhealthy against any HTTP probe. Readiness
# therefore belongs to whatever starts the controller, which knows it is starting
# a controller and knows the PEEN_HTTP_LISTEN_ADDRESS it chose. See
# docs/deployment.md for the probe to configure there.
