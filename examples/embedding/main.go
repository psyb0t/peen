// Command embedding runs one Peen turn inside this process against any
// OpenAI-compatible endpoint and prints the answer.
//
//	OPENAI_API_KEY=... go run ./examples/embedding \
//	    -base-url https://api.openai.com/v1 -model gpt-5-mini \
//	    -workspace . -message "What is this project?"
//
// The agent gets Peen's full tool set in the workspace, with your account's
// access, so point it at a directory you are happy for it to read and change.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/drivers/openai"
	"github.com/psyb0t/peen/pkg/peen"
)

const (
	//nolint:gosec // The name of the variable that holds the key, not a key.
	apiKeyEnvironmentVariable = "OPENAI_API_KEY"
	modelReferencePrefix      = "example/"
	configDirectoryPattern    = "peen-embedding-"
	defaultBaseURL            = "https://api.openai.com/v1"
	defaultMessage            = "What is the project in this directory?"
	exitFailure               = 1
)

type arguments struct {
	baseURL   string
	model     string
	workspace string
	message   string
}

func main() {
	os.Exit(execute())
}

// execute runs the example and returns the process exit code, so every
// deferred cleanup runs before main exits.
func execute() int {
	args := arguments{}
	flag.StringVar(&args.baseURL, "base-url", defaultBaseURL,
		"OpenAI-compatible API base URL")
	flag.StringVar(&args.model, "model", "", "model ID at that endpoint")
	flag.StringVar(&args.workspace, "workspace", ".",
		"directory the agent works in")
	flag.StringVar(&args.message, "message", defaultMessage,
		"what to ask the agent")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	answer, err := run(ctx, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return exitFailure
	}

	if _, err := fmt.Fprintln(os.Stdout, answer); err != nil {
		return exitFailure
	}

	return 0
}

func run(ctx context.Context, args arguments) (string, error) {
	if args.model == "" {
		return "", ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet, "-model")
	}

	// The config directory holds optional SYSTEM.md and harness files. A
	// throwaway one keeps the example self-contained.
	configDirectory, err := os.MkdirTemp("", configDirectoryPattern)
	if err != nil {
		return "", ctxerrors.Wrap(err, "create config directory")
	}
	defer removeDirectory(configDirectory)

	reference := modelReferencePrefix + args.model
	client := elelem.New(openai.NewDriver(
		openai.WithBaseURL(args.baseURL),
		openai.WithAPIKey(os.Getenv(apiKeyEnvironmentVariable)),
	))

	//nolint:contextcheck // peen.New takes no context; Message below does.
	runtime, err := peen.New(peen.Options{
		ConfigDirectory: configDirectory,
		Workspace:       args.workspace,
		InMemoryStore:   true,
		Models: map[string]peen.ModelClient{
			reference: {Client: client, Model: elelem.Model{ID: args.model}},
		},
		DefaultModel: reference,
	})
	if err != nil {
		return "", ctxerrors.Wrap(err, "start peen")
	}

	result, err := runtime.Message(ctx, peen.MessageRequest{
		Message: args.message,
	})

	if closeErr := runtime.Close(context.WithoutCancel(ctx)); closeErr != nil {
		return "", ctxerrors.Wrap(closeErr, "close peen")
	}

	if err != nil {
		return "", ctxerrors.Wrap(err, "run turn")
	}

	return result.Message, nil
}

func removeDirectory(directory string) {
	if err := os.RemoveAll(directory); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
