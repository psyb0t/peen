package main

import (
	"testing"

	workerpkg "github.com/psyb0t/peen/internal/pkg/worker"
	"github.com/stretchr/testify/assert"
)

// The framework leaves appName defaulting to its own name and expects every
// build to override it through -ldflags. A plain `go build ./cmd`, `go run`, or
// `go install` passes none, so without the init in init.go this binary would
// introduce itself as Servicepack in its own help and in every log line. These
// tests run against exactly that build.
func TestBinaryIdentifiesItselfAsPeen(t *testing.T) {
	t.Parallel()

	assert.Equal(t, productName, appName)
	assert.NotEqual(t, servicepackDefaultName, appName)
}

func TestRootCommandIsNamedForTheProduct(t *testing.T) {
	t.Parallel()

	rootCmd := buildRootCommand()

	assert.Equal(t, productName, rootCmd.Use)
	assert.Equal(t, productName, rootCmd.Short)
}

// The worker command is internal. It stays hidden so `peen --help` presents the
// control plane, which is the only role a person starts by hand.
func TestWorkerCommandStaysHidden(t *testing.T) {
	t.Parallel()

	rootCmd := buildRootCommand()

	for _, command := range rootCmd.Commands() {
		if command.Name() != workerpkg.WorkerCommand {
			continue
		}

		assert.True(t, command.Hidden, "the worker command must stay hidden")

		return
	}

	t.Fatalf("the %q command is not registered", workerpkg.WorkerCommand)
}
