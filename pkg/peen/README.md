# Embedding Peen

`pkg/peen` runs the durable agent engine inside a Go process. A `Runtime` owns SQLite, in-process workflow workers, jobs, child agents, event delivery and harness resolution. A `Session` addresses one conversation. The engine starts no HTTP listener or Servicepack services and reads no `PEEN_` environment variables.

[`examples/embedding`](../../examples/embedding/main.go) is a complete program that runs one turn against any OpenAI-compatible endpoint.

The [embedding guide](https://psyb0t.github.io/peen/embedding/) covers sessions, replay, subscriptions, custom tools, hooks, and filesystems. The [option mapping](https://psyb0t.github.io/peen/embedding-options/) lists the deployment equivalent of each option.
