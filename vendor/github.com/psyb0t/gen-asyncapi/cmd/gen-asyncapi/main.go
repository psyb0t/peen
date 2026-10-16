// Command gen-asyncapi generates Go or TypeScript types, strict JSON
// decoding and a client from an AsyncAPI 3 document:
//
//	gen-asyncapi --config=config.yaml asyncapi.yaml
//
// or from a go:generate directive next to the output:
//
//	//go:generate go tool gen-asyncapi --config=config.yaml asyncapi.yaml
//
// The -language, -package and -o flags override the config file, and they
// can stand in for it entirely.
package main

import (
	"context"
	"flag"
	"io"
	"os"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/gen-asyncapi/internal/codegen"
	_ "github.com/psyb0t/slogging/slogconf" // configures slog from the LOG_* environment
)

const (
	outputFileMode  = 0o644
	exitCodeFailure = 1
	//nolint:lll // one usage line, splitting it changes the printed text
	usageText = "usage: gen-asyncapi [--config=config.yaml] [-language=go|typescript] [-package=name] [-o=output.go] asyncapi.yaml"
)

func main() {
	ctx := context.Background()

	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		ctxscope.GetLogger(ctx).Error("generation failed", "err", err)
		os.Exit(exitCodeFailure)
	}
}

// options is what the command line asks for.
type options struct {
	configPath  string
	language    string
	packageName string
	outputPath  string
	specPath    string
}

func parseOptions(
	ctx context.Context,
	args []string,
	usageOutput io.Writer,
) (options, error) {
	flags := flag.NewFlagSet("gen-asyncapi", flag.ContinueOnError)
	flags.SetOutput(usageOutput)
	flags.Usage = func() {
		if _, err := io.WriteString(usageOutput, usageText+"\n"); err != nil {
			ctxscope.GetLogger(ctx).Warn("write usage failed", "err", err)
		}

		flags.PrintDefaults()
	}

	var parsed options

	flags.StringVar(
		&parsed.configPath,
		"config",
		"",
		"generator config file",
	)
	flags.StringVar(
		&parsed.language,
		"language",
		"",
		"go or typescript, overrides the config",
	)
	flags.StringVar(
		&parsed.packageName,
		"package",
		"",
		"Go package name, overrides the config",
	)
	flags.StringVar(
		&parsed.outputPath,
		"o",
		"",
		"output file, overrides the config",
	)

	if err := flags.Parse(args); err != nil {
		return options{}, ctxerrors.Wrap(err, "parse flags")
	}

	if flags.NArg() != 1 {
		flags.Usage()

		return options{}, ctxerrors.Wrapf(
			commerr.ErrInvalidArgument,
			"want one spec path, got %d arguments",
			flags.NArg(),
		)
	}

	parsed.specPath = flags.Arg(0)

	return parsed, nil
}

func run(ctx context.Context, args []string, usageOutput io.Writer) error {
	parsed, err := parseOptions(ctx, args, usageOutput)
	if err != nil {
		return err
	}

	config, err := loadConfig(parsed.configPath)
	if err != nil {
		return err
	}

	if parsed.language != "" {
		config.Language = codegen.Language(parsed.language)
	}

	if parsed.packageName != "" {
		config.Package = parsed.packageName
	}

	if parsed.outputPath != "" {
		config.Output = parsed.outputPath
	}

	if err := config.Validate(); err != nil {
		return ctxerrors.Wrap(err, "validate config")
	}

	source, err := codegen.Generate(config, parsed.specPath)
	if err != nil {
		return ctxerrors.Wrapf(err, "generate from %s", parsed.specPath)
	}

	if err := os.WriteFile(config.Output, source, outputFileMode); err != nil {
		return ctxerrors.Wrapf(err, "write %s", config.Output)
	}

	ctxscope.GetLogger(ctx).Info(
		"generated",
		"spec", parsed.specPath,
		"output", config.Output,
		"size_bytes", len(source),
	)

	return nil
}

func loadConfig(path string) (*codegen.Config, error) {
	if path == "" {
		return &codegen.Config{}, nil
	}

	config, err := codegen.LoadConfig(path)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load config")
	}

	return config, nil
}
