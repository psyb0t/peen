# peen

`pkg/peen` is Peen's stable, supported programmatic entry point. It embeds
the same durable agent runtime the HTTP service exposes, without starting
Servicepack or an HTTP listener.

`New` binds the process working directory to one durable session. Every
`Message` and `Stream` call uses that session, including after a restart with
the same state directory and working directory.

When `Message` or `Stream` reaches a local active turn,
`MessageResult.Queued` is true and `Message` is empty. The existing turn owns
the eventual provider response. Queued input cannot override model or
system-prompt settings, and it is not replayed automatically after a restart.

A child agent the model launches with `launchAgent` runs on its own goroutine and can keep running after `Message` or `Stream` returns. `Stream` forwards a child's events only while the turn that launched it runs. When a child of the root ends, its answer waits as a session event, and a new turn starts for it when the runtime is idle. `Cancel` stops the active turn and every running child. See [running children](../../docs/agents.md#running-children).

## Embedding example

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/drivers/openai"
	"github.com/psyb0t/peen/pkg/peen"
)

func main() {
	driver, err := openai.NewDriver(openai.WithAPIKey("your-token-here"))
	if err != nil {
		log.Fatal(err)
	}

	const modelReference = "openai/gpt-4o-mini"

	runtime, err := peen.New(peen.Options{
		ConfigDirectory: "/var/lib/peen", // must already exist
		RootAgent:       "core:general-purpose",
		Models: map[string]peen.ModelClient{
			modelReference: {
				Client: elelem.New(driver),
				Model:  elelem.Model{ID: "gpt-4o-mini"},
			},
		},
		DefaultModel:     modelReference,
		MaxContextTokens: 32768,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	result, err := runtime.Message(ctx, peen.MessageRequest{
		Message: "list the files in the current directory",
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(result.Message)

	// Stream the next turn from the same startup workspace session.
	_, err = runtime.Stream(ctx, peen.MessageRequest{
		Message: "now summarize what you found",
	}, func(event peen.Event) error {
		fmt.Printf("%s: %s\n", event.Type, event.Payload)

		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}
```

## What this package does not expose

`pkg/peen` never leaks transcript storage internals, generated HTTP types,
concrete tool implementations, Servicepack services, or provider
credentials. Its exported method set is deliberately small: `Message`,
`Stream`, `ListMessages`, `Session`, and `Cancel`.
