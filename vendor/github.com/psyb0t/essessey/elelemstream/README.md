# elelemstream — elelem's callbacks, as content blocks

[elelem](https://github.com/psyb0t/elelem) streams a turn as callbacks: deltas
arrive, then tool calls, then their results. It has no notion of a content
block index, and no reason to — that is a wire concern.

This package is the seam. `Adapter` registers the callbacks, tracks where the
turn is, and emits correctly-indexed blocks through an `essessey.Publisher`.

```go
pub := essessey.NewPublisher(ctx, sink)
adapter := elelemstream.New(pub)

resp, err := adapter.Bind(
    elelem.NewRequest(client).WithModel(model).WithPrompt(prompt),
).Run(ctx)
```

## Adding your own callbacks

An app usually has per-round concerns of its own — a heartbeat, a log line, a
metric. Register them as well; elelem's `On*` setters append to a chain, so
both the `Adapter`'s callback and yours run:

```go
adapter.Bind(req).
    OnRoundStart(func(context.Context, *elelem.RoundEvent) error {
        heartbeat.Touch()

        return nil
    })
```

This needs elelem **v0.3.0 or later**. Before that the setters replaced rather
than appended, and registering your own hook silently unregistered the
`Adapter`'s — the stream just stopped emitting blocks, with no error to catch.

## When elelem is not streaming

`elelem.WithStreaming(false)` exists for backends that cannot serve a streaming
call — an async job queue in front of the model, typically. It changes the
transport, not the callbacks: elelem feeds the finished response through the
same `On*` hooks the `Adapter` is bound to.

So this package keeps working, and the block protocol on the wire is the same
shape. The only difference a subscriber sees is timing — the blocks for a turn
all arrive at once, at the end, instead of filling in as the model writes.

Every callback is also exported (`OnRoundStart`, `OnDelta`,
`OnAssistantMessage`, `OnRoundEnd`, `OnToolCallStart`, `OnToolResult`), for
when you want your hook at an exact point relative to the `Adapter`'s, or want
to wire only some of them:

```go
req.OnRoundStart(func(ctx context.Context, ev *elelem.RoundEvent) error {
    heartbeat.Touch()

    return adapter.OnRoundStart(ctx, ev)   // yours first, then the Adapter's
})
```

## Why this is a package and not ten lines in your app

Because the index arithmetic is fiddly and silently wrong when you get it
slightly off. A tool result landing on the wrong index does not error — it
renders into the wrong card, and you find out from a screenshot.

## The arithmetic

A round emits its thinking/text blocks first. Wherever those stop becomes
`toolBase`, and every tool block in the round is placed relative to it:

```
toolBase + i                    tool_use for call i
toolBase + toolCallCount + i    tool_result for call i
toolBase + 2*toolCallCount      where the NEXT round starts
```

All tool_use blocks come before any tool_result block — which is why the
result offset carries `+ toolCallCount` rather than pairing each result
directly after its call. With two parallel calls the round lays out as:

| index | block |
|---|---|
| `toolBase + 0` | tool_use, call 0 |
| `toolBase + 1` | tool_use, call 1 |
| `toolBase + 2` | tool_result, call 0 |
| `toolBase + 3` | tool_result, call 1 |

Naive implementations interleave use/result pairs and look correct for a
single tool call. Parallel calls are where they break.

## Invariants

- **Blocks open lazily.** A round with no reasoning must not emit an empty
  thinking block, and must not consume an index for one.
- **Thinking closes before text opens.** They are separate blocks; overlapping
  them makes the client render reasoning into the answer.
- **The index advances only on a block that actually opened.** `Close` on an
  unopened streamer is a no-op — otherwise a silent round shifts everything
  after it.
- **The round advances once, on the LAST tool result** (`i == toolCallCount-1`),
  not per result.

Enforcement lives in `adapter_test.go`, which asserts the emitted indices for
the single-call, parallel-call, non-zero-`toolBase` and second-round cases.
Those tests were mutation-checked: breaking either the result offset or the
round advance fails them.

## What it deliberately does not do

Heartbeats, persistence, chat identifiers, logging of application state —
those belong to the app that owns the turn, not to the translation layer. This
package converts callbacks into blocks and nothing else.

## Errors

`ErrRoundStreamNotInitialized` and `ErrToolResultMissing` name the two
callback-wiring faults, wrapped with call-site context, so `errors.Is` works
through the wrap. Both mean the request was built without `Bind`, or elelem
handed over a result-less tool event.
