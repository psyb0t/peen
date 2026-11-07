package agent

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/stretchr/testify/require"
)

const (
	// rootConversation keys the root conversation's script. A child's script
	// is keyed by the name of the agent that sent it its task.
	rootConversation = "root"
	// compactionConversation keys the compaction model's calls.
	compactionConversation = "compaction"
	// taggedTaskPrefix opens every child's first message.
	taggedTaskPrefix = `<message from="agent" name="`

	launchAgentWaitCallID = "call_wait"
	launchAgentWaitBound  = `{"timeout":"30s"}`

	launchAgentShutdownBound = time.Minute
	launchAgentPollInterval  = 10 * time.Millisecond

	// agentFinishedEventType and agentFailedEventType name the events
	// package types for tests whose event slice shadows the package.
	agentFinishedEventType = events.TypeAgentFinished
	agentFailedEventType   = events.TypeAgentFailed
)

// conversationDriver serves each conversation in a session from its own
// script, so a parent and the children running beside it each get their
// turns in order however their calls interleave.
//
// A gate holds a conversation's calls until another conversation has started
// a number of calls, which makes the order of events across them
// deterministic.
type conversationDriver struct {
	mutex   sync.Mutex
	scripts map[string]*elelemtest.ScriptedDriver
	gates   map[string]conversationGate
	calls   map[string]int
	changed chan struct{}

	// systems remembers which conversation each system prompt belongs to. A
	// compaction can summarize a child's task away, and its system prompt is
	// what still names the conversation afterwards.
	systems map[string]string
}

type conversationGate struct {
	after string
	calls int
}

func newConversationDriver(
	scripts map[string]*elelemtest.ScriptedDriver,
) *conversationDriver {
	return &conversationDriver{
		scripts: scripts,
		gates:   map[string]conversationGate{},
		calls:   map[string]int{},
		changed: make(chan struct{}),
		systems: map[string]string{},
	}
}

// route names the conversation of one request and remembers its system
// prompt for later requests whose task a compaction removed.
func (d *conversationDriver) route(request elelem.DriverRequest) string {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	key := conversationKey(request)
	system := requestSystemPrompt(request)

	if key != rootConversation {
		d.systems[system] = key

		return key
	}

	if learned, found := d.systems[system]; found {
		return learned
	}

	return key
}

// after holds conversation's calls until the after conversation has started
// calls calls.
func (d *conversationDriver) after(
	conversation string,
	after string,
	calls int,
) *conversationDriver {
	d.gates[conversation] = conversationGate{after: after, calls: calls}

	return d
}

func (d *conversationDriver) script(key string) *elelemtest.ScriptedDriver {
	script, ok := d.scripts[key]
	if !ok {
		// An unscripted conversation fails its first call, which is what a
		// test expecting no such conversation wants to see.
		return elelemtest.NewScriptedDriver()
	}

	return script
}

func (d *conversationDriver) requests(key string) []elelem.DriverRequest {
	return d.script(key).Requests()
}

func (d *conversationDriver) begin(
	ctx context.Context,
	request elelem.DriverRequest,
) *elelemtest.ScriptedDriver {
	key := d.route(request)

	for {
		d.mutex.Lock()
		gate, gated := d.gates[key]
		open := !gated || d.calls[gate.after] >= gate.calls
		changed := d.changed

		if open {
			d.calls[key]++
			close(d.changed)
			d.changed = make(chan struct{})
			d.mutex.Unlock()

			return d.script(key)
		}

		d.mutex.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return d.script(key)
		}
	}
}

func (d *conversationDriver) Stream(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	return d.begin(ctx, request).Stream(ctx, request, onDelta)
}

func (d *conversationDriver) Complete(
	ctx context.Context,
	request elelem.DriverRequest,
	onDelta func(elelem.Delta) error,
) (elelem.Usage, error) {
	return d.begin(ctx, request).Complete(ctx, request, onDelta)
}

func (d *conversationDriver) ListModels(
	ctx context.Context,
) ([]string, error) {
	return d.script(rootConversation).ListModels(ctx)
}

func (d *conversationDriver) Capabilities(
	model elelem.Model,
) elelem.Capabilities {
	return d.script(rootConversation).Capabilities(model)
}

//nolint:ireturn // elelem.Driver fixes this signature.
func (d *conversationDriver) TokenCounter() elelem.TokenCounter {
	return d.script(rootConversation).TokenCounter()
}

// conversationKey names the conversation a request belongs to: a compaction
// model call, the agent that sent a child its task, or the root.
func conversationKey(request elelem.DriverRequest) string {
	if strings.Contains(
		requestSystemPrompt(request),
		strings.TrimSpace(defaultCompactionPrompt),
	) {
		return compactionConversation
	}

	for _, message := range request.Messages {
		if message.Role != elelem.RoleUser {
			continue
		}

		rest, isTask := strings.CutPrefix(message.Text(), taggedTaskPrefix)
		if !isTask {
			return rootConversation
		}

		name, _, _ := strings.Cut(rest, `"`)

		return name
	}

	return rootConversation
}

// launchAndWaitTurns scripts a conversation that launches one child, waits
// for it, and then answers final.
func launchAndWaitTurns(
	t *testing.T,
	callID string,
	input launchAgentInput,
	final string,
) []elelemtest.Turn {
	t.Helper()

	return []elelemtest.Turn{
		elelemtest.ToolCall(
			callID,
			toolNameLaunchAgent,
			launchAgentArguments(t, input),
		),
		elelemtest.ToolCall(
			launchAgentWaitCallID+"_"+callID,
			toolNameWaitMessages,
			launchAgentWaitBound,
		),
		elelemtest.Text(final),
	}
}

// launchedChildDriver runs a root that launches one child and waits for it,
// and a child answering with childTurns once the root is waiting.
func launchedChildDriver(
	t *testing.T,
	input launchAgentInput,
	childTurns ...elelemtest.Turn,
) *conversationDriver {
	t.Helper()

	return newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(
			launchAndWaitTurns(t, launchAgentCallID, input, "done")...,
		),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(childTurns...),
	}).after(runtimeTestAgentName, rootConversation, 2)
}

// taggedTask is the first message a child launched by the root receives.
func taggedTask(t *testing.T, task string) string {
	t.Helper()

	rendered, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: runtimeTestAgentName,
		Run:  rootAgentRunReference,
		Text: task,
	})
	require.NoError(t, err)

	return rendered
}

// onlyDeliveredNotice returns the single session event a turn delivered.
func onlyDeliveredNotice(t *testing.T, turnEvents []Event) events.Notice {
	t.Helper()

	payload := sessionEventsPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, turnEvents, EventTypeSessionEvents),
		&payload,
	))
	require.Len(t, payload.Notices, 1)

	return payload.Notices[0]
}

// eventsOfTypes keeps the events of the given types, in order.
func eventsOfTypes(turnEvents []Event, types ...string) []string {
	kept := make([]string, 0, len(turnEvents))

	for _, event := range turnEvents {
		if slices.Contains(types, event.Type) {
			kept = append(kept, event.Type)
		}
	}

	return kept
}

// heldChildDriver runs a root whose child never gets past its first model
// call until its context ends, so the child is still running when the test
// acts on it.
func heldChildDriver(
	t *testing.T,
	rootTurns ...elelemtest.Turn,
) *conversationDriver {
	t.Helper()

	return newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(rootTurns...),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(
			elelemtest.Text("never sent"),
		),
	}).after(runtimeTestAgentName, rootConversation, math.MaxInt)
}

// onlyRun waits until the session holds exactly one child run and returns it.
func onlyRun(
	t *testing.T,
	fixture runtimeFixture,
	sessionID func() uuid.UUID,
) *AgentRun {
	t.Helper()

	var run *AgentRun

	require.Eventually(t, func() bool {
		id := sessionID()
		if id == uuid.Nil {
			return false
		}

		registry := fixture.runtime.existingAgentRuns(id)
		if registry == nil {
			return false
		}

		runs := registry.List()
		if len(runs) != 1 {
			return false
		}

		run = runs[0]

		return true
	}, launchAgentShutdownBound, launchAgentPollInterval)

	return run
}

// wrappedCancelError satisfies errors.Is(err, context.Canceled) without needing a
// real cancelled context.Context to construct, since finishAgentRun's
// decision reads parentCtx.Err() separately from the error value itself.
type wrappedCancelError struct{}

func (w *wrappedCancelError) Error() string { return context.Canceled.Error() }

func (w *wrappedCancelError) Is(target error) bool { return target == context.Canceled }
