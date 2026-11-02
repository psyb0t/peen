FROM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437

ENV GOTOOLCHAIN=local \
    GOFLAGS=-mod=mod

WORKDIR /src
COPY server/ ./
RUN go build -o /usr/local/bin/answer-server . && rm -rf /src /root/.cache

WORKDIR /app
COPY workspace/ ./
RUN chmod 755 /app/run-server.sh
