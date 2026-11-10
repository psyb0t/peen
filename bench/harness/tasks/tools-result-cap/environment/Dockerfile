FROM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437

WORKDIR /app
COPY gen/ /tmp/gen/
RUN python3 /tmp/gen/inventory.py && rm -rf /tmp/gen
COPY workspace/ ./
COPY scenario/ /scenario/
