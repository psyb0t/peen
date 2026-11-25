---
title: "REST API"
description: "Read stored sessions, messages, turns, jobs, and child agents from Peen, and control active work, with every /api/v1 endpoint."
nav_order: 10
---

# REST API

Use REST to read what Peen stored or to control a run. Use the [WebSocket API](websocket-api.md) to send messages and watch turns as they happen. Under `/api/v1`, the only call that can start a turn is [`POST /api/v1/session/notices`](https://psyb0t.github.io/peen/api/publish-session-notice/), and only when an event handler lets that notice wake an idle session. Outside `/api/v1`, the optional [OpenAI API](openai-api.md) and [MCP control server](mcp-server.md) run turns, one request per turn, and [scheduled turns](cron.md) and the [Telegram bot](telegram.md) start turns too.

## The reference

The contract is [api/api.yml](https://github.com/psyb0t/peen/blob/main/api/api.yml) (OpenAPI 3.1). The documentation site renders it as one page per operation, grouped by area under **REST API** in the sidebar. Each page lists the operation's parameters, request body, every response with its fields, the errors it returns and when, and a curl example. Download the [OpenAPI file](https://psyb0t.github.io/peen/reference/api.yml) to generate a client. Peen's embedded browser control surface is served at `/` and is built from the same generated types.

The curl examples read three shell variables. Set `PEEN_URL` to your controller's address (the examples fall back to `http://localhost:8080`), `PEEN_API_TOKEN` to the token described in [network exposure](security.md#network-exposure), and `PEEN_SESSION_ID` to a session ID from `POST /api/v1/sessions/open` or `GET /api/v1/sessions`. Replace placeholders such as `$JOB_ID` and `$AGENT_RUN_ID` with IDs from your own instance.

## Requests and responses

Every operation is mounted under `/api/v1`. `Authorization: Bearer <token>` is required only when `PEEN_API_TOKEN` is set. Bodies and responses are JSON, and a request whose `Accept` header rules out JSON gets `406`. The controller checks every request against the spec and answers one that does not match with `400 VALIDATION_FAILED`.

Every response carries an `X-Request-ID` header. Operations under `/api/v1/session` and `/api/v1/messages` name their session with the `X-Session-ID` request header, and their successful responses echo it, as does `POST /api/v1/sessions/open`. [Terms](architecture.md#terms) explains how the session ID, request ID, turn ID, and `X-Request-ID` differ.

Every session of one workspace shares one agent space. [Opening a session](https://psyb0t.github.io/peen/api/open-session/) with a different agent space returns `409 AGENT_SPACE_CONFLICT`, and [reconfiguring](https://psyb0t.github.io/peen/api/reconfigure-session/) moves every session of the workspace together. [Where it runs](where-it-runs.md) explains agent spaces.

Lists page with `limit` (1 to 200, default 50) and `offset` (default 0), and report `hasMore`. The [feed](https://psyb0t.github.io/peen/api/list-session-feed/), [job output](https://psyb0t.github.io/peen/api/read-session-job-output/), and [child agent events](https://psyb0t.github.io/peen/api/list-session-agent-run-events/) page with a `cursor` and return `nextCursor` instead, so a client that reconnects continues without a gap or a duplicate.

Errors use one envelope:

```json
{
  "code": "SESSION_NOT_FOUND",
  "message": "session not found"
}
```

`code` is stable and `message` is for people. An operation that takes `X-Session-ID` answers `404 SESSION_NOT_FOUND` when the session does not exist, and `404 NOT_FOUND` when the session exists but the job, agent run, or other record the path names does not. [API error codes](troubleshooting.md#api-error-codes) lists every code and what to do about it.

## Workspace files

Four operations read and change files in a session's workspace: [list a directory](https://psyb0t.github.io/peen/api/list-workspace-files/), [read a file](https://psyb0t.github.io/peen/api/read-workspace-file/), [write a file](https://psyb0t.github.io/peen/api/write-workspace-file/), and [delete a file](https://psyb0t.github.io/peen/api/delete-workspace-file/). Each needs `X-Session-ID`, every path is relative to the session's stored workspace, and file content travels as base64.

A path must be clean and relative, as `path.Clean` would leave it. An absolute path, `..`, a leading `./`, a trailing `/`, a doubled `/`, `.`, or a NUL byte returns `403 WORKSPACE_NOT_ALLOWED`, and so does an empty path on read, write, or delete. A symbolic link that leads out of the workspace and the system session get the same answer. A path that does not exist, including one that runs through a regular file such as `README.md/notes`, returns `404 NOT_FOUND`. A directory listing shows every symbolic link as `type: symlink` without following it, including one that points outside the workspace. Reads and writes are limited to `PEEN_MAX_FILE_API_BYTES`, 16 MiB by default. A write replaces the file atomically, and `createDirectories` creates missing parent directories. A delete removes one file, symbolic link, or empty directory, never follows a link, and never deletes a directory's contents.

```bash
curl --request PUT \
  --header "Authorization: Bearer $PEEN_API_TOKEN" \
  --header "X-Session-ID: $PEEN_SESSION_ID" \
  --header "Content-Type: application/json" \
  --data '{"path": "notes/todo.txt", "contentBase64": "aGVsbG8K", "createDirectories": true}' \
  "${PEEN_URL:-http://localhost:8080}/api/v1/session/files/content"
```

## Scheduled turns

[`GET /api/v1/cron/jobs`](https://psyb0t.github.io/peen/api/list-cron-jobs/) lists the configured jobs with their next occurrence and last run, and [`GET /api/v1/cron/runs`](https://psyb0t.github.io/peen/api/list-cron-runs/) pages the run history, newest scheduled occurrence first. Neither needs a session header. History stays readable with scheduling off and includes jobs removed from configuration. A run's `error` is one of the fixed reasons in [runs and history](cron.md#runs-and-history). See [scheduled turns](cron.md).
