// Package peen is Peen's stable, supported programmatic entry point.
//
// It embeds the same durable agent runtime the HTTP service exposes, without
// starting Servicepack or an HTTP listener. A caller builds Options (a config
// directory holding the durable SQLite store plus optional SYSTEM.md,
// APPEND_SYSTEM.md, and COMPACTION.md overrides, a default workspace, a root
// agent, a named Elelem upstream registry, and qualified main and compaction
// model references), constructs one Runtime with New, and then:
//
//   - collects a final answer for one turn with Message;
//   - does the same while observing events as they happen with Stream;
//   - reads the workspace's stored transcript with ListMessages;
//   - reads the workspace's metadata with Details;
//   - requests cancellation of the active turn with Cancel.
//
// No call takes or returns a session identifier. A Runtime is one workspace and
// owns its durable session privately. Session IDs exist so a controller can
// route between many workspaces, which a direct caller never has to do.
//
// New never reads environment variables, starts Servicepack, opens an HTTP
// listener, or touches global state; every dependency arrives through
// Options. This package does not expose transcript storage internals,
// generated HTTP types, concrete tool implementations, Servicepack services,
// or provider credentials. See README.md for a runnable embedding example.
package peen
