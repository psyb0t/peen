# peen

`pkg/peen` is Peen's stable, supported programmatic entry point. It embeds
the same durable agent runtime the HTTP service exposes, without starting
Servicepack or an HTTP listener.

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
	driver, err := openai.NewDriver(openai.WithAPIKey("sk-..."))
	if err != nil {
		log.Fatal(err)
	}

	const modelReference = "openai/gpt-4o-mini"

	runtime, err := peen.New(peen.Options{
		ConfigDirectory: "/var/lib/peen", // must already exist
		RootAgent:       "default",
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

	// Resume the same session and stream the next turn's events as they
	// happen.
	_, err = runtime.Stream(ctx, peen.MessageRequest{
		Message:   "now summarize what you found",
		SessionID: result.SessionID,
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
