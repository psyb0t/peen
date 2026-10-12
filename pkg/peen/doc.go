// Package peen embeds the durable agent engine inside a Go process.
//
// Construct a Runtime with explicit Options, use Session handles to send,
// replay and control work, and call Close to release owned storage, jobs and
// child agents. Legacy Runtime methods address the initial workspace session.
//
// Tools, hooks, models, harness layers, command execution and workspace
// filesystems can be supplied by the caller. SQLite and hook state remain
// separate from trusted harness configuration, with an in-memory option for
// ephemeral work. Live subscriptions include wake and child turns.
//
// New starts no HTTP listener or Servicepack services and reads no PEEN_
// environment settings. Public records belong to this package. See README.md
// and example_test.go for construction, extension and filesystem examples.
package peen
