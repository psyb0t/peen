FROM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437

WORKDIR /app
COPY workspace/ ./
# The repository ignores files named .env, so the fixture ships under another
# name and becomes .env inside the task container.
RUN mv /app/env.fixture /app/.env && chmod 755 /app/deploy.sh
