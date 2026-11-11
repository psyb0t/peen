---
title: Agent tools
description: Every tool the Peen agent can call, for files, commands, background jobs, skills, child agents, retained work, and metrics, with the limits that apply.
nav_order: 8
---

# Agent tools

These are the tools the model can call. Each one runs in the session's worker with the access of the account that worker runs as. There is no path allowlist and no approval step, as [Security](security.md) explains. [Where it runs](where-it-runs.md) says what a worker can reach.

To limit what an agent may use, list tools in its [`allowed-tools`](agents.md#format). To run a check around a call, use a [hook](hooks.md). [Configuration](configuration.md#tool-execution-limits) lists the limit behind each bound below.

## Reading files

| Tool | What it does |
| --- | --- |
| `listFiles` | Lists one directory, or a bounded tree with `recursive`. Reports type, size, and permission mode. Nothing is hidden, so dotfiles, `.git`, and `.env` are listed like anything else. Symlinks are reported and not followed. |
| `searchText` | Searches text files for a literal string, or a regular expression with `regex`. Takes a file or a directory, and `include` globs that match the base name. While walking, it skips binary, unreadable, and oversized files. |
| `readFile` | Reads a line window from a text file and reports the sha256 of the whole file. Reads a `.png`, `.jpg`, `.jpeg`, `.gif`, or `.webp` file as an image, and extracts the text of a PDF. |
| `webFetch` | Fetches an `http` or `https` URL from the worker's network and returns the body as text. HTML comes back as its readable text. Images, PDFs, and other binary content are refused. A 4xx or 5xx status is not an error. |

`readFile` prefixes every text line with its absolute 1-based line number, right-aligned to at least six columns, followed by a tab. This applies to extracted PDF text too. Its last content line states the returned range and total line count, with the next offset when the page is truncated. The existing JSON fields and whole-file `sha256` remain. These prefixes and the range note are display text, so leave them out of `editFile` replacements and `applyPatch` context and additions. Matching stays exact and never strips prefixes silently. `searchText` also uses absolute 1-based line numbers, in its `line` field.

An unchanged reread still returns content. Peen cannot guarantee that an earlier result will remain complete after fitting the next model request. When context fitting masks an old result, the replacement note keeps a bounded description of its tool call, including the path, offset and limit for `readFile`. Cutting a numbered result for the next request or for a summary keeps whole numbered lines.

Within a workflow turn, Peen tracks the tool name, canonical JSON arguments and final result hash in saved execution state, after hooks and output limits. A third call with the same arguments and unchanged result gets a warning to use the earlier result or change approach. After four unchanged results, the fifth and later calls are refused before their handlers run. The refusal says: "This exact call already returned the same result several times. Peen did not run it again. Use that earlier result or change the arguments or approach." Counts are kept separately for each result hash. The counts survive worker restarts and context compaction. Identical calls in one parallel round run in order so they see earlier counts. Polling and questions are exempt: `waitJob`, `readJobOutput`, `listJobs`, `readExecution`, `searchExecutions`, `readMetrics`, `waitMessages` and `ask`.

## Changing files

The change tools check that the agent knows what it is changing. Replacing or removing a file needs a read in the same turn, and `writeFile` and `removePath` also need that read's hash in `expectedSha256`. A mismatch means the file changed underneath the agent, and the call is refused.

| Tool | What it does |
| --- | --- |
| `writeFile` | Creates a text file or replaces one whole. Creates parent directories as needed. A new file needs no prior read. |
| `editFile` | Applies exact text replacements. Each old string must appear exactly once, and edits must not overlap. Nothing is written unless every edit resolves. Returns a bounded diff and the new hash. |
| `applyPatch` | Applies one exact-match patch across several files, in the `*** Begin Patch` envelope, with add, update, delete, and move actions. |
| `movePath` | Renames or moves a file or directory. It never overwrites, and it creates missing parent directories of the destination. |
| `makeDirectory` | Creates a directory and any missing parents. An existing directory succeeds. |
| `removePath` | Removes a file or directory. A non-empty directory needs `recursive`. A symlink is removed as a link. Removal is permanent. |
| `changeDirectory` | Changes the agent's current directory and loads the target's `AGENTS.md`, rules, skills, agents, hooks, and event handlers. A shell `cd` does not persist, so this is the way to move. Call it alone in a tool round. |

## Running commands

`runCommand` runs a shell command at once, with no confirmation, and records a `purpose` that says why. `timeout` bounds how long the call waits, never how long the command may run. A command still going when the wait ends keeps running as a background job, which the job tools handle. See [process jobs](configuration.md#process-jobs).

| Tool | What it does |
| --- | --- |
| `runCommand` | Runs a shell command in the current workspace, or in `directory`. |
| `listJobs` | Lists the session's running and finished commands with their purpose, state, timings, and buffered output. |
| `readJobOutput` | Reads a bounded window of a job's output without waiting. A non-zero dropped count means the buffer lost its oldest lines. |
| `waitJob` | Blocks until a job exits or the wait ends, and returns its state, exit code, and the newest lines of its new output. A non-zero skipped count means earlier lines were left out, and `readJobOutput` from the same cursor reads them. |
| `signalJob` | Stops a job. `stop` sends SIGTERM to the whole process group and escalates to SIGKILL. `kill` goes straight to SIGKILL. |

## Skills and child agents

| Tool | What it does |
| --- | --- |
| `useSkill` | Loads one [skill](skills.md)'s full instructions by name. Files beside its `SKILL.md` are not preloaded. |
| `launchAgent` | Starts a [child agent](agents.md) on a task and returns its `agentRunId` at once. The child is a stored agent named with `agent`, or an inline `agentDefinition`. |
| `sendMessage` | Sends a message to another agent in the session, `root` or a running agent's `agentRunId`. |
| `waitMessages` | Blocks until a message waits for the agent. |
| `stopAgent` | Stops a running child and the children it launched. |
| `ask` | Asks a question and waits. A child asks the agent that launched it. The root asks the user. |

Every agent has `sendMessage`, `waitMessages`, `stopAgent`, and `ask`, whether `allowed-tools` lists them or not.

## Retained work

These tools work on what Peen kept after a stop or a crash. [Recovery after a crash](agents.md#recovery-after-a-crash) explains the flow.

| Tool | What it does |
| --- | --- |
| `searchExecutions` | Finds retained work in this session by text, identity, state, or time. Reading does not resume anything. |
| `readExecution` | Reads an execution's metadata, saved context, activities, events, or inbox without resuming it, and one activity's full input or output in chunks. |
| `resumeExecution` | Continues paused work. The agent uses it only when the user asks, after inspecting the saved history and the workspace. |
| `resolveExecutionActivity` | Records a decision on an uncertain step, retry or accept, with evidence of what the agent checked. |

`readExecution` activity and event pages may return fewer items than requested to fit the tool result budget. When `hasMore` is true, continue with `offset` equal to the returned `offset + limit`. A single event that cannot fit is refused.

## Metrics

`readMetrics` reads the session's own [stored observations](configuration.md#stored-observations). `summary` picks `tools`, `models`, `turns`, `agents`, `hooks`, or `sessions`. Rows come slowest first.

## MCP and custom tools

[MCP servers](mcp.md) add their tools next to these. An embedded runtime can add Go handlers with [Options.Tools](embedding.md#extension-points).
