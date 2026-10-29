# Benchmarks

These runs test Peen as a harness: whether a turn survives hundreds of tool calls, provider errors, long contexts, and compaction, and whether the records it keeps explain what happened. Scores depend on the model as much as on Peen, so the useful output is the list of Peen defects each run found, which the [CHANGELOG](../CHANGELOG.md) records as fixed.

## How the runs work

Each task runs in its own benchmark container through [Harbor](https://github.com/laude-institute/harbor), with Peen's static binary copied in and started with `peen exec` in the `control` agent space.

- The task container has no network, drops every Linux capability except the `CHOWN`, `DAC_OVERRIDE` and `FOWNER` the benchmark's root grader needs, and sets `no-new-privileges`. Peen runs as uid 10001 and holds no capabilities. A probe recorded per trial confirms no network route, no Docker socket, and no host home directory.
- Peen reaches the model through a loopback relay to a Unix socket. A proxy outside the container holds the provider key and forwards only to the Z.ai coding endpoint and only for the two allowed models, logging the status, duration, and size of every request.
- The model is `zai/glm-5.3`, with `zai/glm-5.3-flash` writing compaction summaries. `PEEN_MAX_CONTEXT_TOKENS` is `1000000`, GLM-5.3's window, unless a run says otherwise. Logs run at debug level.
- Each trial keeps Peen's WebSocket frames, its debug log, and its SQLite state, so every model call, tool call, compaction, and turn end can be checked afterwards.
- Every benchmark first runs its reference solutions under the same sandbox, so a grader that cannot work offline is not counted against Peen.

## October 2026, Peen v0.32.0

### Aider Polyglot

29 of the 87 Aider Polyglot tasks whose tests run offline: C++, Go, and Rust tasks without crate dependencies. Java, JavaScript, Python, and two Rust tasks download test tools or crates while grading, so they cannot be graded without network.

| Language | Passed |
| --- | --- |
| C++ | 7 of 9 |
| Go | 6 of 10 |
| Rust | 5 of 10 |
| Total | 18 of 29 (62%) |

One Rust trial failed because of a Peen defect (the retry bug below). The other ten were the model's code: wrong output, a missing exported function, and Rust borrow and lifetime errors. Every other turn ended normally. A task averaged about 16 model calls and 20 tool calls, taking 1 to 11 minutes.

### Long-Horizon Terminal-Bench

Four offline tasks from [LHTB](https://github.com/zli12321/lhtb), each run once with the full 1M context and once with a 128K context to force compaction. Stock Harbor runs each task as one turn, without LHTB's continue-until-timeout loop, so these are single-turn scores.

| Task | Budget | 1M | 128K |
| --- | --- | --- | --- |
| `langchain-version-migration` | 90 min | 0.33 | 0.33 |
| `rush_hour_campaign` | 60 min | 0.04 | 0.04 |
| `snake_maze_campaign` | 60 min | ran out of time | ran out of time |
| `sudoku-recovery` | 90 min | ran out of time | ran out of time |

- `langchain-version-migration` made 91 tool calls at 1M and 180 at 128K before the agent declared the work done.
- In `rush_hour_campaign` the model spent its second reply, 65,384 of 65,536 output tokens, on reasoning and was cut off at the output limit.
- `snake_maze_campaign` and `sudoku-recovery` worked until the time limit, with 46 to 116 model calls and prompts up to 175K tokens.

### Defects found

| Defect | Found in | Status |
| --- | --- | --- |
| A turn failed on the round after a model call that succeeded on retry, because the saved turn context could not decode the failed attempt's error. | Aider, a Z.ai `500` | Fixed in v0.33.0 |
| `PEEN_TURN_TIMEOUT` limited each model request, not the turn, so a long task ran past it to the grader's time limit. | LHTB | Renamed `PEEN_MODEL_REQUEST_TIMEOUT` in v0.33.0 |
| A reply cut off at the output limit completed with empty text and no sign it was cut off. | LHTB `rush_hour_campaign` | `turn.completed` carries `finishReason` in v0.33.0 |
| `summarize` compaction summarizes only turns that have ended, so inside one long turn it leaves out the oldest rounds without a summary. In the 128K `langchain-version-migration` run it did so 34 times. | LHTB at 128K | Open |
| go-workflows instance lookups took 220 to 525 ms under SQLite contention. | 3 of 29 Aider trials | Open |

## Earlier runs

### Terminal-Bench 2.0, two tasks

`cancel-async-tasks` and `regex-log`, with both GLM models, in the same sandbox, on v0.32.0 development builds. Each cell gives glm-5.3, then glm-5.3-flash.

| Build | Context | `cancel-async-tasks` | `regex-log` |
| --- | --- | --- | --- |
| before the fix | 32K default | 0.0, 1.0 | 1.0, 1.0 |
| before the fix | 1M | both turns failed | 1.0, 1.0 |
| with the fix | 1M | 1.0, 1.0 | 1.0, 1.0 |

The 1M run found that a tool step saved the whole turn context two or three times, so any context past about 40K tokens exceeded the stored message limit and failed the turn. A tool step now saves the context once, under its own 12 MiB limit. The glm-5.3 miss at the 32K default was the model's solution, with 5 of 6 tests passing.

### Long context

A local task that reads 32 files of 30 KB each in one turn and reports a key from each. Both models scored 1.0 with prompts of about 218K tokens and stored contexts of about 1.1 MB, past the old 1 MiB limit, with no warnings.

### Compaction recall

Seven turns build up facts under a 24K budget that forces several compactions, then an eighth turn asks for 20 of them.

| Version | glm-5.3 | glm-5.3-flash |
| --- | --- | --- |
| Before summary room sizing | 0 of 20, no compaction could run | 20 of 20 |
| v0.32.0 | 20 of 20 (the scorer missed one hyphenated answer, scored 19) | 20 of 20 |

The first glm-5.3 run found that a fixed summary room never fit a 24K context, so no summary was ever written. `PEEN_COMPACTION_SUMMARY_PERCENT` now sizes the room as a share of the context and halves it when it does not fit.

## Running them

The runner is Harbor 0.24 with a Peen agent adapter that copies in the binary and a relay, and a small proxy that holds the key. Each benchmark's reference solutions run first under the same sandbox. Put the provider key only in the proxy, never in the task container, and give each trial's Peen its own state directory so the SQLite records can be read afterwards.
