//go:build real

package realtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealHarnessExecutesTheFullCodingLifecycle(t *testing.T) {
	configured := realConfig(t)
	upstreams, err := configured.Upstreams()
	require.NoError(t, err)

	providerName, modelID, found := strings.Cut(configured.DefaultModel, "/")
	require.True(t, found)
	require.NotEmpty(t, providerName)
	require.NotEmpty(t, modelID)

	selectedUpstream, found := upstreamByName(upstreams, providerName)
	require.True(t, found)
	require.NotEmpty(t, selectedUpstream.BaseURL)

	fixture := newRealHarnessFixture(t)
	proxy := newRealHarnessProviderProxy(
		t,
		selectedUpstream.BaseURL,
		fixture.service,
	)
	t.Cleanup(proxy.Close)

	for index := range upstreams {
		if upstreams[index].Name == selectedUpstream.Name {
			upstreams[index].BaseURL = proxy.URL()
		}
	}

	encodedUpstreams, err := json.Marshal(upstreams)
	require.NoError(t, err)

	binary := buildRealHarnessBinary(t)
	process := startRealHarnessPeen(t, binary, fixture, string(encodedUpstreams), configured.DefaultModel)
	t.Cleanup(func() { process.stop(t) })

	// The session opens on the service directory, not the fixture root. The
	// task names paths relative to it, and the deepest AGENTS.md layer only
	// resolves for a session rooted there.
	sessionID := openRealHarnessSession(t, process, fixture.service)
	require.Equal(t, sessionID, sendRealHarnessMessage(t, process, sessionID))

	messages := listRealHarnessMessages(t, process.baseURL, process.apiToken, sessionID)
	t.Logf("real harness tool calls: %v", realHarnessToolCallNames(messages))
	assertRealHarnessTranscriptMessages(t, messages)
	assertRealHarnessTranscript(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessAgentRun(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessJob(t, process.baseURL, process.apiToken, sessionID, fixture.service)
	assertRealHarnessInjectedEventDelivered(t, process.baseURL, process.apiToken, sessionID)
	assertRealHarnessFiles(t, fixture)

	generationID := assertRealHarnessWorkerGeneration(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
	)
	turnIDs := assertRealHarnessDurableTurns(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
		realHarnessLifecycleTurns,
	)
	assertRealHarnessDurableEvents(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		turnIDs,
		generationID,
	)
	assertRealHarnessModelRuns(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		turnIDs,
		modelID,
	)
	assertRealHarnessTurnSnapshots(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
	)

	publishRealHarnessWakeEvent(t, process.baseURL, process.apiToken, sessionID)
	waitForRealHarnessWake(t, process.baseURL, process.apiToken, sessionID)

	// The wake turn runs in the same generation. A controller that restarted
	// the worker between the two turns would show a second row here.
	assert.Equal(t, generationID, assertRealHarnessWorkerGeneration(
		t,
		process.baseURL,
		process.apiToken,
		sessionID,
		fixture.service,
	))

	assertRealHarnessProviderContext(t, proxy, fixture.service)
	assertRealHarnessAuditLog(t, fixture.auditDirectory)
}

func TestRealHarnessFixtureResolvesAllInstructionLayers(t *testing.T) {
	fixture := newRealHarnessFixture(t)
	resolver, err := harness.NewResolver(
		fixture.configDirectory,
		harness.DefaultLimits(),
	)
	require.NoError(t, err)

	snapshot, err := resolver.Resolve(fixture.service)
	require.NoError(t, err)
	blocks, err := snapshot.PromptBlocks(realHarnessRootAgent)
	require.NoError(t, err)

	contents := make([]string, 0, len(blocks))
	for _, block := range blocks {
		contents = append(contents, block.Content)
	}

	prompt := strings.Join(contents, "\n")
	for _, marker := range []string{
		realHarnessConfigRuleMarker,
		realHarnessWorkspaceRuleMarker,
		realHarnessProjectsRuleMarker,
		realHarnessCatalogRuleMarker,
		realHarnessServiceRuleMarker,
	} {
		assert.Contains(t, prompt, marker)
	}
}

func TestRealHarnessHookHelper(_ *testing.T) {
	if os.Getenv(realHarnessHookHelperEnv) != realHarnessHookHelperEnabled {
		return
	}

	invocation := struct {
		Event          string          `json:"event"`
		Input          json.RawMessage `json:"input"`
		StateDirectory string          `json:"stateDirectory"`
	}{}
	if err := json.NewDecoder(os.Stdin).Decode(&invocation); err != nil {
		os.Exit(1)
	}

	if invocation.StateDirectory == "" {
		os.Exit(1)
	}

	marker := filepath.Join(invocation.StateDirectory, realHarnessReadMarker)
	switch invocation.Event {
	case "postReadFile":
		if err := os.WriteFile(marker, []byte("observed"), realHarnessFileMode); err != nil {
			os.Exit(1)
		}
	case "preApplyPatch":
		if _, err := os.Stat(marker); err != nil {
			if _, writeErr := fmt.Fprint(
				os.Stdout,
				`{"decision":"deny","reason":"Read the target before applying a patch."}`,
			); writeErr != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}

		if _, err := fmt.Fprint(os.Stdout, `{"decision":"allow"}`); err != nil {
			os.Exit(1)
		}
	case "preToolUse":
		input := struct {
			Command string `json:"command"`
		}{}
		if err := json.Unmarshal(invocation.Input, &input); err != nil {
			os.Exit(1)
		}

		if !realHarnessCommandAllowed(input.Command) {
			if _, err := fmt.Fprintf(
				os.Stdout,
				`{"decision":"deny","reason":%q}`,
				realHarnessCommandOnlyReason,
			); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}

		if _, err := fmt.Fprint(os.Stdout, `{"decision":"allow"}`); err != nil {
			os.Exit(1)
		}
	default:
		os.Exit(1)
	}
}
