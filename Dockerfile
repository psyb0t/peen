# Production Dockerfile - Multi-stage build
FROM golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df AS builder

ARG BUILD_COMMIT=""
ARG BUILD_VERSION="dev"

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
# main.appName, which cmd/main.go uses as cobra's Use:/Short:. Without it the
# binary falls back to the literal "servicepack" and introduces itself by the
# framework's name in its own --help.
RUN APP_NAME="$(head -n 1 go.mod | awk '{print $2}' | awk -F'/' '{print $NF}')" && \
    CGO_ENABLED=0 go build -a \
    -ldflags "-X main.appName=${APP_NAME} -X main.buildCommit=${BUILD_COMMIT} -X main.buildVersion=${BUILD_VERSION}" \
    -o ./build/app ./cmd

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
RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        ripgrep \
        tini \
        tzdata \
        unzip && \
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

# Switch to non-root user
USER appuser

# tini reaps the processes run_command spawns and forwards signals to the app.
ENTRYPOINT ["/usr/bin/tini", "--", "./app"]

# Default command if no args provided
CMD ["--help"]
