---
title: Scheduled turns
description: Run scheduled Peen turns in regular workspace sessions, inspect their durable history, and bound unattended provider spending.
nav_order: 13
---

# Scheduled turns

Set `PEEN_CRON=true` and write `PEEN_CONFIG_DIR/.agents/cron.yaml`. Peen reads this operator file once at startup. With cron off, it ignores the file. A missing file or an empty `jobs` list schedules nothing. An invalid job stops startup and names the job and field. Restart Peen after editing it.

```yaml
jobs:
  - name: nightlyReport
    schedule: "0 9 * * *"
    timeZone: UTC
    workspace: /srv/work/reports
    session: continue
    message: "Review the workspace for {jobName} at {scheduledAt}."
    timeout: 30m
```

The workspace must already exist under `PEEN_WORKSPACE_ROOT` and pass the same private-directory checks as an API-opened session. Each scheduled turn runs in a regular session of that workspace, never the system session. Scheduled sessions have their own history, separate from the workspace's [canonical session](architecture.md#sessions-and-entry-points), and run in the workspace's one agent space, which every session of that workspace shares.

## Job fields

| Field | Meaning |
| --- | --- |
| `name` | Unique name, 1 to 64 ASCII letters, digits, underscores or hyphens. |
| `schedule` | Five cron fields, optional leading seconds, or a descriptor such as `@daily` or `@every 30m`. Intervals must use positive whole seconds. |
| `timeZone` | IANA timezone, default `UTC`. Set it here rather than embedding `TZ=` or `CRON_TZ=` in the schedule. |
| `workspace` | Absolute directory under the workspace root. |
| `agentSpace` | Optional configured agent space. Left out, the job runs in the workspace's agent space, or the controller's default when the workspace has no session yet. Set, it must match the workspace's agent space and every other job's `agentSpace` for the same workspace, or startup stops and names the jobs, the workspace and the agent spaces. |
| `session` | `continue` reuses one session for this job, the default. `new` creates separate history on every run. Changing a job definition resets its continuation. |
| `message` | Nonempty prompt within `PEEN_MAX_MESSAGE_BYTES`, including expansion. `{jobName}` becomes the name; `{scheduledAt}` becomes the scheduled UTC RFC 3339 timestamp. |
| `model` | Optional configured model ID. |
| `reasoningEffort` | Optional `minimal`, `low`, `medium`, `high`, `xhigh` or `max`. |
| `timeout` | Optional whole-second duration up to `24h`. An omitted or zero value uses `PEEN_TURN_TIMEOUT`. |
| `outputSchema` | Optional inline YAML mapping containing a JSON Schema within `PEEN_MAX_OUTPUT_SCHEMA_BYTES`. External references are refused. |
| `telegramChatId` | Optional notification recipient. A nonzero value requires [Telegram](telegram.md) to be enabled and the chat to be allowlisted. |

The configuration is bounded to 1 MiB and 256 jobs. Unknown fields, duplicate names or keys, multiple YAML documents, invalid models, impossible schedules and excessive aliases or nesting are refused.

## Runs and history

A still-running job records its next tick as `skipped`. On restart Peen records one `skipped` row for the latest missed occurrence and does not replay missed work. The stored schedule baseline and unique occurrence index prevent a tick from firing twice. Calendar schedules follow the named timezone, including daylight saving changes.

A run in progress is `running`. Runs end as `completed`, `failed` or `cancelled`; overlap and downtime use `skipped`. A turn that finishes without valid structured output keeps its completed turn and `outputError`, while its scheduled run is `failed`. If the workspace's agent space was changed after startup so that it no longer matches the job, the run is recorded as `failed`. History keeps a fixed `error` reason without copying prompts, provider errors or schemas: `scheduled run failed`, `scheduled turn failed`, `scheduled turn cancelled`, `scheduled turn timed out`, `previous run still running`, `controller was offline`, `scheduled turn interrupted`, `workspace unavailable`, `session unavailable`, `workspace agent space conflict` or `scheduled output invalid`. Treat any other value as an unknown failure. Shutdown stops new dispatch and uses the normal turn shutdown and crash recovery lifecycle.

Read jobs and history through the [HTTP API](http-api.md#scheduled-turns); history stays readable while scheduling is off. Each run's `sessionId` opens the conversation it produced, at `http://127.0.0.1:8080/?session=<sessionId>` in the control surface or through `GET /api/v1/messages` with `X-Session-ID`. The controller log records a `scheduled run started` and a `scheduled run finished` line for each run.

Scheduled turns run unattended and spend provider budget. See [security](security.md#scheduled-turns) for bounding them and for who can change this file.

## Telegram notifications

Set `telegramChatId` to an allowed chat to send a finished run's status and output to that chat. A reply continues the run's session in this workspace's agent space, so anyone allowed in that chat drives it. See [scheduled notifications](telegram.md#scheduled-notifications).
