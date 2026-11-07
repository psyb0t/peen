//go:build integration

package api_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/psyb0t/peen/tests/testinfra"
)

const (
	apiBasePath           = "/api/v1"
	messagesPath          = apiBasePath + "/messages"
	sessionPath           = apiBasePath + "/session"
	sessionCancelPath     = sessionPath + "/cancel"
	sessionFeedPath       = sessionPath + "/feed"
	modelRunsPath         = sessionPath + "/model-runs"
	headerAuthorization   = "Authorization"
	headerContentType     = "Content-Type"
	headerSessionID       = "X-Session-ID"
	bearerPrefix          = "Bearer "
	jsonMediaType         = "application/json"
	integrationTimeout    = 25 * time.Minute
	requestTimeout        = 30 * time.Second
	apiTestRestartTimeout = 30 * time.Second

	apiTestWorkspaceRulesFile   = "AGENTS.md"
	apiTestInitialRules         = "Initial image workspace rules."
	apiTestUpdatedRules         = "Updated image workspace rules."
	apiTestRestartMessage       = "prove durable image history"
	apiTestReloadMessage        = "prove image harness reload"
	apiTestQueuedSessionMessage = "create a queueable session"
	apiTestActiveTurnMessage    = "hold this active turn"
	apiTestQueuedMessage        = "queue this message for the active turn"
	apiTestQueuedPageLimit      = 4

	apiTestNestedInstructionsFile = "service/AGENTS.md"
	apiTestNestedInstructions     = "Nested service instructions."
	apiTestNestedScopeLine        = "Instructions from service/AGENTS.md. They apply to files under service/"
	apiTestNestedMessage          = "prove unvisited nested instructions stay unloaded"
)

// integrationInfra is the production stack TestMain starts once for the suite.
//
//nolint:gochecknoglobals // TestMain owns it and every test reads it.
var integrationInfra *testinfra.Infra

func TestMain(m *testing.M) {
	setupContext, cancelSetup := context.WithTimeout(
		context.Background(),
		integrationTimeout,
	)

	infra, err := testinfra.Setup(setupContext)
	cancelSetup()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "set up Peen API integration test:", err)
		os.Exit(1)
	}
	integrationInfra = infra

	exitCode := m.Run()
	teardownContext, cancelTeardown := context.WithTimeout(
		context.Background(),
		integrationTimeout,
	)
	if err := integrationInfra.Teardown(teardownContext); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "tear down Peen API integration test:", err)
		if exitCode == 0 {
			exitCode = 1
		}
	}
	cancelTeardown()

	os.Exit(exitCode)
}
