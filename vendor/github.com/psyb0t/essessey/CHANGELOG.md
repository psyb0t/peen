# Changelog

All notable changes per release. Versions follow [semver](https://semver.org)
pre-1.0 conventions: minor bumps may include breaking API changes (called out
explicitly), patch bumps are docs / build / fixes only.

## v0.7.5 — 2026-09-09

Bug fix. No breaking API change.

- `Reassemble` now retains streamed `thinking` blocks and their deltas in
  `ParsedStream.Thinking`, instead of dropping them as unknown content.
- The reassembly timeline now emits `TimelineKindThinking`, preserving the
  order of reasoning, text, and completed tool executions.

## v0.7.4 — 2026-08-22

CI change. No API change.

- Restored the GitHub Release on tag pushes. The move to `code-workflow` dropped
  the release step that `go-workflow` ran inline; a `release` job now calls the
  reusable `release-workflow` once the checks pass on a tag.
- Removed the leftover `actions: read` grant, which the reusable `code-workflow`
  no longer requests.

## v0.7.3 — 2026-08-21

Build and CI change. No API change.

Moves the pipeline to the container-backed `code-workflow` and pins the Go
toolchain in a dev image, which clears the stale-toolchain standard-library
vulnerability (GO-2026-5972) that the loose `setup-go` pin exposed on the
scheduled scan.

- Added `Dockerfile.dev` (Go 1.26.6, govulncheck, semgrep) and routed
  `make lint`, `make test-coverage`, and the new `make sec` through it.
- `pipeline.yml` now calls `code-workflow` with a govulncheck plus semgrep
  security gate and SARIF upload, replacing `go-workflow`.
- Anchored the coverage ignore rule to the repository root so it no longer hid
  `scripts/test-coverage.sh` or the vendored `x/text` `coverage.go` files; those
  files are now tracked, so a vendored build is complete.

## v0.7.2 — 2026-08-08

Dependency migration. No API change.

- Log scope now comes from `github.com/psyb0t/ctxscope` instead of
  `github.com/psyb0t/common-go/scope`. That package was extracted into its own
  module so it can ship on its own schedule rather than one shared with a module
  that also carries gorm, echo, NATS and the Temporal SDK. The API is unchanged
  apart from the package name — every call site moved from `scope.X` to
  `ctxscope.X`.
- No exported signature here mentions a scope type, so this package's own API is
  untouched.

## v0.7.1 — 2026-08-08

Repository infrastructure only. No library code changed.

- Added the imported-by badge: a count of the public packages importing this
  module, linking to `importers.md` on the `badges` branch — the importing
  repositories, grouped, package counts descending, and flagged when the owner
  differs from this repo's.
- It measures **blast radius, not adoption**. The number tells you how much
  breaks when an exported name moves; the external mark tells you whether any of
  that is someone else's problem, which is what decides how strictly the module
  has to be versioned. It currently reads `0`, which is honest and useful in its
  own right: nothing downstream breaks yet.
- Refreshed weekly rather than daily, because pkg.go.dev's crawl lags
  publication by days and each run drags the full test suite along (the badges
  job needs the coverage artifact). The whole pipeline runs rather than a
  badges-only job: the badge publisher republishes only what a run produced, so
  a badge-only refresh would delete the coverage, version and license badges.
- The cron slot is derived from a hash of the repository name rather than
  chosen — GitHub cron has no randomness, and its scheduler sheds queued runs
  hardest at the round times a human would pick.

## v0.7.0 — 2026-08-06

One naming change, applied everywhere: what this library streams is a **stream**,
not a conversation.

### Changed

- **Breaking.** `MessageMeta.ConversationID` is now `MessageMeta.StreamID`, and
  its JSON tag moves from `conversation_id` to `stream_id`. This is on the wire,
  so a client reading `message_start` must read the new key.

- **Breaking.** `ParsedStream.ConversationID` is now `ParsedStream.StreamID`.

- The `conversationID` parameter of `Publisher.SendMessageStart` and
  `Publisher.SendStreamPreamble` is renamed to `streamID`. Positional callers are
  unaffected — the rename is source-compatible for anyone passing arguments by
  position, which is every caller of a Go function.

  Migration is mechanical: `conversation_id` → `stream_id` on the wire,
  `.ConversationID` → `.StreamID` in Go.

  The rationale is that nothing in this library is chat-specific. It streams
  content blocks — a conversation turn, a build log, a document render, a
  long-running job. Naming the identifier after one caller's domain made every
  other use read as a workaround. Applications keep their own vocabulary and map
  it onto `streamID` at the boundary.

  This also resolves an incoherence introduced in v0.6.0: `EventStore` already
  keyed retention by `streamID` while the wire called the same value
  `conversation_id`, with nothing stating they were the same thing.

### Added

- The `message_start` test now pins the RAW JSON key, not just the Go field.
  Decoding into `MessageStartData` only proves the tag round-trips through
  itself — it passes just as happily with the wrong tag, while clients read the
  raw key. The test now asserts `stream_id` is present and `conversation_id` is
  absent.

- `EventStore`'s documentation now says what `streamID` actually is: the same
  identifier `SendMessageStart` puts on the wire, opaque to this library, minted
  by the caller. It also states the two things that were previously implicit —
  that keying retention at a finer grain than the wire id is a legitimate trade
  (smaller buffer, no replay of earlier turns), and that a reconnecting client
  supplies the `streamID` itself, so `Since` resolves it as a map key with no
  notion of ownership. Authorize a resume exactly as you authorize opening the
  stream.

## v0.6.1 — 2026-08-06

Documentation and error context. No API change, no behaviour change.

### Added

- A README for each binding, written for someone who does not already know how
  this works. [sse](sse/README.md) covers the HTTP handler end to end, what the
  bytes literally look like — including a multi-line payload becoming one
  `data:` field per line — plus reading, keep-alives and resuming. It leads with
  the trap that costs the most time: **this package does not set
  `Content-Type: text/event-stream`, and a browser `EventSource` fails the
  connection without it.** [nats](nats/README.md) covers the subject scheme with
  real wildcard examples and states plainly that the event id is not carried on
  that binding at all. [ws](ws/README.md) covers the one-concurrent-writer rule
  that this package cannot enforce for you.

- A "Reading a stream back" section in the root README. Every example there
  showed how to WRITE a stream; `Reassemble` — half of what this library does —
  had no example anywhere.

### Fixed

- **Ten call sites returned an error without context**, so a failure inside a
  composite call such as `SendToolUseBlock` arrived with no frame naming which
  step failed. They now wrap with `ctxerrors`, matching what the rest of the
  codebase already did.

- The root README's file layout listed every file except `store.go` and
  `multisink.go`, the two added in v0.6.0, so the retention API appeared not to
  exist. `ErrInvalidCapacity` was likewise undocumented.

### Known limitation

`InMemoryEventStore` is bounded per stream but **unbounded in the NUMBER of
streams**: the map grows one entry per `streamID` ever seen and only `Clear`
removes it, so total retention is `streams × capacity`. Call `Clear` when a
stream ends, or sweep periodically — nothing evicts a whole stream on age or
count.

## v0.6.0 — 2026-08-06

Retention and fan-out, so the event ids added in v0.5.0 can actually be used to
resume a dropped stream. Additive — nothing existing changes behaviour.

### Added

- `MultiSink` — fans one `Emit` out to several sinks. Every other sink is
  terminal, so there was previously no way to send the same event to two
  places. It forwards to all of them even when one fails, returning the joined
  error: a full store or a broken audit sink must not cost the client its
  stream, but the failure must not vanish either. A non-nil error therefore
  means "at least one destination missed it", not "nothing was delivered".

- `EventStore` — retains recent events per stream so a reconnecting client can
  be resumed. Its read side reports whether the resume point is KNOWN rather
  than guessing: when an id has aged out or never existed, replaying from the
  start duplicates everything the client already rendered, and replaying from
  now silently drops the events in between — which is the exact gap ids exist
  to prevent. Only the caller can decide, so `Since` hands that decision back.

- `InMemoryEventStore` — a bounded, per-stream, concurrency-safe default. Per
  stream it keeps a ring buffer for order and eviction plus an id index for
  lookup; ids are opaque strings with no ordering, so a ring alone would make
  every resume a linear scan. `SinkFor(streamID)` is its write half, so
  retention composes with `MultiSink` instead of needing a wrapper type.

### Notes

Replay deliberately introduces no new code path: ask the store what came after
the client's last id, wrap it in the existing `SliceSource`, and pump it through
the ordinary `Sink`. A separate replay path is a path that can drift from the
live one, and a test asserts the two are indistinguishable.

Behaviours pinned by tests rather than left implicit:

- Events with no id are retained and replayed, but cannot be resumed TO —
  dropping them would silently skip real content.
- A duplicate id moves the resume point to the later occurrence, skipping what
  came between. Ids are assumed unique per stream.
- Eviction removes the evicted id from the index, so a long-dead id never
  reports as a valid resume point and the index cannot grow without bound.
- A capacity of zero or less is refused at construction — it would accept every
  append and resume nothing, a buffer that silently never works.

Two things the store cannot do for you, documented in the README: replay must go
through the wire sink alone (replaying into the `MultiSink` re-appends what it is
replaying), and `streamID` is a security boundary — replay keyed only by event id
would hand one client another's events.

## v0.5.0 — 2026-08-06

The SSE binding now implements the wire format as specified, instead of the
subset one consumer happened to exercise. **Breaking** for anyone who depended
on the old framing bytes or the old parser's behaviour — details below.

### Fixed

- **The codec could not round-trip its own output.** A sink wrote a payload of
  `{\n"a":1\n}` and the matching source read back `{`: truncated at the first
  newline, remainder discarded, no error. Any payload containing a newline was
  silently corrupted end to end.

- **A payload containing a blank line forged a second event.** The framer wrote
  the whole payload as ONE `data:` line, but the format has no escaping and no
  length prefix — a newline inside a value ends the field and a blank line ends
  the event. Payloads are now emitted as one `data:` field per line, which is
  how the format represents a multi-line value.

- **An event type containing a newline could inject events.** Same missing
  split on the `event:` line: one `Emit` call could put several complete,
  caller-chosen events on the wire. Field values that cannot survive a single
  line (CR, LF, NUL) are now stripped before framing.

- **Conformant streams from other producers were unreadable.** The parser
  matched the literal prefix `"data: "`, space included. That space is OPTIONAL
  in the format, so a producer writing `data:x` yielded nothing at all — no
  event, no error. Field lines are now parsed properly: name before the first
  colon, value after, exactly one leading space removed if present.

- **Multiple `data:` fields lost everything after the first.** They are joined
  with newlines into a single value, which is the only way a multi-line payload
  survives.

- **Events were emitted on the wrong trigger.** The parser paired an `event:`
  line with the next `data:` line; the format ends an event at a BLANK LINE.
  With the state machine in place, a lone `retry:` or a block of comments no
  longer produces a phantom event, and an event with no data field is discarded
  rather than delivered empty.

- **A lone CR was not a line terminator**, so a stream using bare CR — which
  the format permits — parsed as nothing at all.

- **A leading byte order mark swallowed the first event**, because it glued
  itself to the first field name.

- **Valid frames were being dropped as malformed.** A `data:` line with no
  preceding `event:` line is a real event whose type is simply absent, and two
  `event:` lines in a row means the last one wins. Both were previously skipped.

### Added

- `Event.ID` — the event identifier, emitted as `id:` and parsed back. This is
  what makes a dropped connection recoverable: a client returns the last id it
  saw as `Last-Event-ID` on reconnect, so a server can resume rather than
  restart, and a subscriber tracking ids can detect a gap instead of silently
  rendering one. Empty is omitted on purpose — an empty `id:` field RESETS the
  receiver's resume point rather than meaning "no id".
- `Source.LastEventID()` and `Source.Retry()` — the stream-level state a client
  needs to reconnect correctly. The last id persists across events until the
  producer changes it; an id containing NUL is ignored, leaving the previous
  one intact so a malformed value cannot destroy a valid resume point.
- `FrameRetry(time.Duration)` — tells the client how long to wait before
  reconnecting. It describes the stream, not an event, so it is its own frame
  rather than a field on `Event`.
- `FrameComment(string)` — the keep-alive an intermediary needs to see so it
  does not drop an idle connection.

### Changed

- **Breaking:** `FrameLines` output differs for any payload containing a
  newline, and for any event carrying an ID. Byte-for-byte comparisons against
  the old output will fail; the new bytes are what the format actually
  specifies.
- **Breaking:** `Source` now delivers events the old parser dropped (typeless
  events) and no longer delivers phantom ones.
- The README no longer claims a NATS subscriber and an `EventSource` receive
  identical JSON. The PAYLOAD is identical; the envelope is not. WebSocket
  sends the whole `Event` as one object, NATS publishes the payload raw with
  the type in the subject and carries no id, SSE carries all three as wire
  fields. A NATS subscriber has the most reconstruction to do, and that is now
  stated rather than implied away.

## v0.4.2 — 2026-08-05

CI only. No code, no API, no behaviour change.

- **This repo was missing three of the four workflows every other public repo
  here runs.** It had `pipeline.yml` and nothing else, so it was never mirrored,
  never archived, and had no PR gate.
  - `mirror-and-archive.yml` — pushes mirror to GitLab and Codeberg; the
    default branch and tags additionally save to the Wayback Machine.
  - `issue-pull.yml` — relays issues opened on the mirrors back here.
  - `collaborators-only.yml` — closes and locks PRs from non-collaborators.
- The two cron slots are **this repo's own**, not copies of another repo's.
  Every mirrored repo holds a unique monthly archive slot, because the Wayback
  save is rate-limited and takes roughly two minutes; reusing a slot would put
  two repos in it. `issue-pull` staggers its minute for the same reason, since
  GitHub fires an account's crons together.

## v0.4.1 — 2026-08-05

README only. No code, no API, no behaviour change.

- The README now reads like the rest of the psyb0t libraries instead of a
  whitepaper. Same facts, same structure, same links, same hill it dies on
  (SSE is a format, not a transport) — just in the voice the rest of the
  ecosystem already uses.

## v0.4.0 — 2026-08-05

Tracks [elelem](https://github.com/psyb0t/elelem) v0.4.0. No API change here.

- **Requires elelem v0.4.0 or later**, and this is the breaking part: upgrading
  essessey pulls elelem v0.4.0 into your build, where `Request.Complete`,
  `Request.Stream` and `Request.CompleteInto` no longer exist. Migration is
  mechanical — `Complete(ctx)` becomes `Run(ctx)`, `CompleteInto(ctx, &v)`
  becomes `RunInto(ctx, &v)`, and `Stream(ctx, fn)` becomes
  `OnDelta(fn).Run(ctx)`. See elelem's changelog for the one behaviour change
  to check for.

- Nothing in this package moved. `Adapter`, `Bind` and every exported callback
  have the same signatures and the same block output.

- `elelem.WithStreaming(false)` — new upstream, for backends that cannot serve
  a streaming call — is transparent here. elelem feeds the finished response
  through the same callbacks the `Adapter` binds, so the blocks on the wire are
  identical; a subscriber only sees them arrive together at the end of the turn
  rather than filling in. Documented in
  [elelemstream/README.md](elelemstream/README.md).

## v0.3.0 — 2026-08-05

`Bind` composes with an app's own callbacks, because the reason it could not
was fixed upstream rather than worked around here.

- **Requires [elelem](https://github.com/psyb0t/elelem) v0.3.0 or later.** Its
  `On*` setters now append to a chain instead of replacing what was
  registered, so an app can call `Bind` and then register its own
  `OnRoundStart` and both run:

  ```go
  adapter.Bind(req).
      OnRoundStart(func(context.Context, *elelem.RoundEvent) error {
          heartbeat.Touch()

          return nil
      })
  ```

  v0.2.0 documented the opposite as a trap to route around. Exporting the
  callbacks made composition possible but still left every caller responsible
  for remembering the ordering rule — a hazard nobody hits until their stream
  silently stops rendering. Fixing the setters upstream removes it for
  everyone instead.

- The exported callbacks stay. They are no longer the workaround, they are the
  way to place a hook at an exact point relative to the `Adapter`'s, or to wire
  only some of them.

- `elelemstream/adapter_test.go` now runs a real turn through a scripted driver
  with an app callback registered after `Bind`, and asserts blocks still reach
  the sink. The old failure mode produced no error at all, so only an
  end-to-end run catches it — verified by removing the `Adapter`'s
  `OnRoundStart` registration and watching the test fail.

## v0.2.0 — 2026-08-05

`elelemstream`'s callbacks are exported, so an app can wrap them.

- **Breaking (in practice, not in signature): `Bind` alone could not serve an
  app with its own per-round concerns.** elelem's `On*` setters REPLACE rather
  than append, so a caller that registered its own `OnRoundStart` after `Bind`
  silently unregistered the adapter's and stopped emitting content blocks
  altogether — no error, just a stream that never renders. Every callback is
  now exported (`OnRoundStart`, `OnDelta`, `OnAssistantMessage`, `OnRoundEnd`,
  `OnToolCallStart`, `OnToolResult`), so an app registers its own hook and
  calls the adapter's from inside it:

  ```go
  req.OnRoundStart(func(ctx context.Context, ev *elelem.RoundEvent) error {
      heartbeat.Touch()
      return adapter.OnRoundStart(ctx, ev)
  })
  ```

  `Bind` stays as the convenience for the case with no extra concerns, and now
  documents the trap rather than leaving it to be discovered.

  Found by doing the first real integration rather than by reading the code —
  the callbacks were unexported, so there was no way to compose at all.

## v0.1.1 — 2026-08-05

Documentation. No API or behaviour change.

- **The core package had no package doc**, so `pkg.go.dev` showed a bare
  symbol list for the package a reader lands on first. Added `doc.go`
  covering the `Event` model, why SSE is treated as a format rather than a
  transport peer, and the lazy-open / index-advance rules that the block
  protocol depends on.
- Added `elelemstream/README.md`. The block-index arithmetic is the one piece
  where being slightly wrong fails silently — a tool result renders into the
  wrong card rather than erroring — so the layout table, the invariants, and
  why parallel tool calls break naive implementations now live next to that
  code instead of only in its tests.
- Reworded the top-level README. Several headings and one table header had
  been carried over verbatim from a sibling project's README rather than
  written for this one.

## v0.1.0 — 2026-08-05

First release. One `Event` — a name plus a JSON payload — streamed to a
client over whichever delivery the caller has, with the wire payload
identical across all of them.

- Core protocol: `Event`, the `Sink`/`Source` interfaces, and `Publisher`
  with one `Send*` method per protocol event (`message_start`,
  `content_block_start/delta/stop`, `message_delta`, `message_stop`, plus
  text, thinking, tool-use and tool-result variants) and the
  `SendStreamPreamble`/`SendStreamEpilogue` pair for opening and closing a
  turn.
- `TextStreamer` and `LineStreamer` accumulate a chunk-at-a-time answer and
  emit correctly-indexed content blocks; both work with a nil `Publisher` for
  non-streaming accumulation.
- `Reassemble` drains a `Source` back into a `ParsedStream`: accumulated
  text, tool calls matched to their results by content-block index, and an
  ordered timeline of both. A malformed or orphaned individual event is
  warn-logged and dropped rather than aborting reconstruction.
- `InMemorySink` and `SliceSource` round-trip a stream with no transport
  involved — feed one's `Events()` into the other.
- `sse`: the SSE format itself — `FrameLines` renders the wire bytes,
  `WriterSink`/`HTTPSink` write framed events to an `io.Writer` or a
  flushing `http.ResponseWriter`, and `Source` scans them back off an
  `io.Reader`.
- `nats` and `ws`: message-oriented bindings that need no framing, each
  defined over the one minimal interface it actually needs from a client
  (`Publish(subject, data)` and `WriteJSON(v)` respectively) rather than
  importing a transport SDK.
- `elelemstream`: bridges [elelem](https://github.com/psyb0t/elelem)'s
  callback stream into this protocol. It is the only subpackage that imports
  elelem — the core package and the `sse`/`nats`/`ws` bindings stay free of
  it.
- Failures are matchable rather than stringly: `ErrNoMoreEvents` ends a
  `Source`, `sse.ErrNotAFlusher` rejects a non-streaming
  `http.ResponseWriter` at construction, and
  `elelemstream.ErrRoundStreamNotInitialized` /
  `elelemstream.ErrToolResultMissing` name the two callback-wiring faults.
  All are wrapped with call-site context, so `errors.Is` works through the
  wrap.
