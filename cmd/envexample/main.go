// Command envexample writes .env.example from the env file template.
//
// It renders the template with no values, which is the commented starting
// point a reader copies to .env. `make generate` runs it through the generate
// directive in internal/pkg/config/envtemplate/gen.go.
package main

import (
	"flag"
	"log"
	"os"

	"github.com/psyb0t/peen/internal/pkg/config/envtemplate"
)

const (
	defaultOutputPath = ".env.example"

	// exampleFileMode is the mode of a tracked file. .env.example holds only
	// fake values, unlike the 0600 .env that `peen config init` writes.
	exampleFileMode = 0o644
)

func main() {
	outputPath := flag.String(
		"out",
		defaultOutputPath,
		".env.example output path",
	)

	flag.Parse()

	rendered, err := envtemplate.Render(envtemplate.Values{})
	if err != nil {
		log.Fatalf("render .env.example: %v", err)
	}

	if err := os.WriteFile(*outputPath, rendered, exampleFileMode); err != nil {
		log.Fatalf("write %s: %v", *outputPath, err)
	}
}
