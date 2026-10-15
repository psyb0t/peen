package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	launchAgentCallID       = "call_launch"
	launchAgentGrandCallID  = "call_grand"
	launchAgentWorkspaceDir = "workspace"
	launchAgentChildName    = "child-agent"
	launchAgentGrandName    = "grandchild-agent"

	launchAgentChildAgentFile             = "---\nname: child-agent\ndescription: test child\n---\nDo the child task."
	launchAgentGrandAgentFile             = "---\nname: grandchild-agent\ndescription: test grandchild\n---\nDo the grandchild task."
	launchAgentRestrictedChildAgentFile   = "---\nname: child-agent\ndescription: test child\nallowed-tools: readFile\n---\nReview only."
	launchAgentDepthLimitedChildAgentFile = "---\nname: child-agent\ndescription: test child\nallowed-tools: readFile, launchAgent\n---\nReview and delegate when possible."
)

// launchAgentFixtureOptions parameterizes newLaunchAgentFixture beyond what
// the shared newRuntimeFixture (runtime_test.go) supports: extra named
// agent files, non-default agent run limits, and a last-word hook over the
// runtime options for settings a test drives rather than accepts.
type launchAgentFixtureOptions struct {
	AgentLimits AgentRunLimits
	AgentFiles  map[string]string
	Customize   func(*RuntimeOptions)
}

// newLaunchAgentFixture builds a runtime the same way newRuntimeFixture
// does, reusing its unexported helpers (writeRuntimeFile, NewStaticRegistry)
// from the same package, but with the extra agent files and agent run
// limits launchAgent's own tests need.
func newLaunchAgentFixture(
	t *testing.T,
	driver elelem.Driver,
	options launchAgentFixtureOptions,
) runtimeFixture {
	t.Helper()

	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	workspace := filepath.Join(root, launchAgentWorkspaceDir)
	require.NoError(t, os.MkdirAll(workspace, runtimeTestDirectoryMode))

	writeRuntimeFile(
		t,
		filepath.Join(configDirectory, ".agents", "agents", runtimeTestAgentName+".md"),
		runtimeTestAgentDocument,
	)

	for name, content := range options.AgentFiles {
		writeRuntimeFile(
			t,
			filepath.Join(configDirectory, ".agents", "agents", name+".md"),
			content,
		)
	}

	resolver, err := harness.NewResolver(configDirectory, harness.Limits{})
	require.NoError(t, err)

	stateDirectory := filepath.Join(root, "state")

	handle, err := db.Open(context.Background(), db.Config{
		Directory: stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	registry, err := NewStaticRegistry(map[string]ModelClient{
		runtimeTestModelReference: {
			Client: elelem.New(driver),
			Model:  elelem.Model{ID: runtimeTestModelID},
		},
	})
	require.NoError(t, err)

	eventBus := events.NewBus(events.Options{})

	runtimeOptions := RuntimeOptions{
		Store:            store,
		Resolver:         resolver,
		Models:           registry,
		RootAgent:        runtimeTestAgentName,
		DefaultModel:     runtimeTestModelReference,
		DefaultWorkspace: workspace,
		MaxContextTokens: runtimeTestMaxContextTokens,
		TurnTimeout:      runtimeTestTurnTimeout,
		Events:           eventBus,
		AgentLimits:      options.AgentLimits,
		ConfigDirectory:  configDirectory,
	}

	if options.Customize != nil {
		options.Customize(&runtimeOptions)
	}

	runtime, err := NewRuntime(context.Background(), runtimeOptions)
	require.NoError(t, err)

	// Children outlive the turn that launched them. Each one has to end, and
	// record that it ended, before the database closes under it.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			launchAgentShutdownBound,
		)
		defer cancel()

		require.NoError(t, runtime.ShutdownAgentRuns(ctx))
	})

	return runtimeFixture{
		runtime:         runtime,
		store:           store,
		handle:          handle,
		workspace:       workspace,
		eventBus:        eventBus,
		configDirectory: configDirectory,
		stateDirectory:  stateDirectory,
	}
}

func launchAgentArguments(t *testing.T, input launchAgentInput) string {
	t.Helper()

	encoded, err := json.Marshal(input)
	require.NoError(t, err)

	return string(encoded)
}

func decodeLaunchAgentOutput(t *testing.T, content string) launchAgentOutput {
	t.Helper()

	output := launchAgentOutput{}
	require.NoError(t, json.Unmarshal([]byte(content), &output))

	return output
}

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

	launchAgentShutdownBound = 30 * time.Second
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

// launchAgent returns as soon as the child runs, naming it. The child's final
// answer reaches the root later as an agent.finished session event whose data
// carries the answer, delivered at the root's next tool boundary.
func TestLaunchAgentReturnsAtOnceAndDeliversTheAnswerAsAnEvent(
	t *testing.T,
) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "say hi", Agent: launchAgentChildName},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	// The root's own stream is ordered. The child's events interleave with it
	// once the root waits, so they are checked apart.
	assert.Equal(
		t,
		[]string{
			EventTypeUserMessageCreated,
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeToolResult,
			EventTypeToolUse,
			EventTypeSessionEvents,
			EventTypeToolResult,
			EventTypeTurnCompleted,
		},
		eventsOfTypes(
			events,
			EventTypeUserMessageCreated,
			EventTypeTurnStarted,
			EventTypeToolUse,
			EventTypeToolResult,
			EventTypeSessionEvents,
			EventTypeTurnCompleted,
		),
	)
	assert.Equal(
		t,
		[]string{
			EventTypeAgentRunStarted,
			EventTypeAgentRunAssistantMessage,
			EventTypeAgentRunCompleted,
		},
		eventsOfTypes(
			events,
			EventTypeAgentRunStarted,
			EventTypeAgentRunAssistantMessage,
			EventTypeAgentRunCompleted,
		),
	)

	use := toolUsePayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolUse),
		&use,
	))
	assert.Equal(t, launchAgentCallID, use.CallID)
	assert.Equal(t, toolNameLaunchAgent, use.Name)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.Equal(t, launchAgentCallID, toolResult.CallID)
	assert.False(t, toolResult.IsError)

	output := decodeLaunchAgentOutput(t, toolResult.Content)
	assert.NotEqual(t, uuid.Nil, output.AgentRunID)
	assert.Equal(t, launchAgentChildName, output.Name)
	assert.Equal(t, AgentRunDefinitionStored, output.Definition)
	assert.Equal(t, rootAgentRunReference, output.ParentAgentRunID)

	notice := onlyDeliveredNotice(t, events)
	assert.Equal(t, agentFinishedEventType, notice.Type)

	data := agentEventData{}
	require.NoError(t, json.Unmarshal(notice.Data, &data))
	assert.Equal(t, output.AgentRunID, data.RunID)
	assert.Equal(t, "child says hi", data.Response)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.Equal(
		t,
		[]string{taggedTask(t, "say hi")},
		tailTexts(childRequests[0].Messages, 1),
		"the child's task names the root as its sender",
	)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	toolMessage := messages.Items[2]
	assert.Equal(t, launchAgentCallID, toolMessage.ToolCallID)
	assert.False(t, toolMessage.IsError)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 1)
	assert.Equal(t, AgentRunStateCompleted, runs[0].Snapshot().State)
	assert.Equal(t, launchAgentCallID, runs[0].ParentToolCallID)

	query := repositories.Use(fixture.handle.GormDB)
	turn, err := query.Turn.WithContext(context.Background()).
		Where(query.Turn.ID.Eq(runs[0].ParentTurnID)).
		First()
	require.NoError(t, err)
	storedRun, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(runs[0].ID)).
		First()
	require.NoError(t, err)
	assert.Equal(t, turn.WorkerGenerationID, storedRun.WorkerGenerationID)
}

// A child launch that succeeds is not an error: nothing in the launch path
// may log at error level, such as a wrap of a nil error. Swaps the process
// default logger, so it must not run in parallel.
func TestLaunchAgentSuccessfulLaunchLogsNoError(t *testing.T) {
	var captured bytes.Buffer

	previousLogger := slog.Default()

	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "say hi", Agent: launchAgentChildName},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	decoder := json.NewDecoder(&captured)

	for decoder.More() {
		record := map[string]any{}
		require.NoError(t, decoder.Decode(&record))
		assert.NotEqual(
			t,
			slog.LevelError.String(),
			record[slog.LevelKey],
			"unexpected error record: %v",
			record,
		)
	}
}

const launchAgentSkillTask = "review the workspace with :review-rules"

func TestLaunchAgentDescriptionTellsTheParentToNameSkillsInTheTask(
	t *testing.T,
) {
	t.Parallel()

	assert.Contains(t, launchAgentDescription, ":name")
	assert.Contains(t, launchAgentDescription, "skill")
	assert.Contains(t, launchAgentDescription, "unknown")
}

// A skill the user named on the parent reaches the child only when the parent
// names it in the task, so a child's system prompt never carries a skill.
func TestLaunchAgentDoesNotCopyTheUsersSkillIntoTheChild(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  "review the workspace",
			Agent: launchAgentChildName,
		},
		elelemtest.Text("child finished"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})
	writeSkillFile(
		t,
		fixture,
		runtimeTestExplicitSkillName,
		runtimeTestWorkspaceSkillInstructions,
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   runtimeTestExplicitSkillMessage,
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.NotContains(
		t,
		childRequests[0].Messages[0].Text(),
		runtimeTestWorkspaceSkillInstructions,
	)
	assert.Equal(
		t,
		[]string{taggedTask(t, "review the workspace")},
		tailTexts(childRequests[0].Messages, 1),
	)

	query := repositories.Use(fixture.handle.GormDB)
	storedRun, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)
	assert.NotContains(
		t,
		storedRun.SystemPrompt,
		runtimeTestWorkspaceSkillInstructions,
	)
}

// The task is treated like a user message: a :name in it brings the skill as
// its own message right after the task, in the child's first request and in
// the child's stored transcript.
func TestLaunchAgentTaskSkillReferenceFollowsTheTaskInTheChildRequest(
	t *testing.T,
) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  launchAgentSkillTask,
			Agent: launchAgentChildName,
		},
		elelemtest.Text("child finished"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})
	writeSkillFile(
		t,
		fixture,
		runtimeTestExplicitSkillName,
		runtimeTestWorkspaceSkillInstructions,
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the reviewer",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	skillMessage := expectedSkillMessage(
		t,
		fixture,
		runtimeTestExplicitSkillName,
	)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.Equal(
		t,
		[]string{taggedTask(t, launchAgentSkillTask), skillMessage},
		tailTexts(childRequests[0].Messages, 2),
	)
	assert.NotContains(
		t,
		childRequests[0].Messages[0].Text(),
		runtimeTestWorkspaceSkillInstructions,
	)
	assert.NotContains(
		t,
		driver.requests(rootConversation)[0].Messages[0].Text(),
		runtimeTestWorkspaceSkillInstructions,
	)

	query := repositories.Use(fixture.handle.GormDB)
	storedRun, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.SessionID.Eq(result.SessionID)).
		First()
	require.NoError(t, err)
	assert.NotContains(
		t,
		storedRun.SystemPrompt,
		runtimeTestWorkspaceSkillInstructions,
	)

	transcript, err := fixture.store.ListAgentRunMessages(
		context.Background(),
		result.SessionID,
		storedRun.ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)
	require.Len(t, transcript.Items, 3)

	contents := make([]string, 0, len(transcript.Items))
	roles := make([]models.MessageRole, 0, len(transcript.Items))

	for _, item := range transcript.Items {
		contents = append(contents, item.Content)
		roles = append(roles, item.Role)
	}

	assert.Equal(
		t,
		[]string{
			taggedTask(t, launchAgentSkillTask),
			skillMessage,
			"child finished",
		},
		contents,
	)
	assert.Equal(
		t,
		[]models.MessageRole{
			models.MessageRoleUser,
			models.MessageRoleUser,
			models.MessageRoleAssistant,
		},
		roles,
	)
}

// A skill the child's task names is checked before the child exists. An
// unknown name fails the launchAgent call and lists the effective skills.
func TestLaunchAgentUnknownTaskSkillFailsTheCall(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "review with :ghost-skill",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})
	writeSkillFile(
		t,
		fixture,
		runtimeTestExplicitSkillName,
		runtimeTestWorkspaceSkillInstructions,
	)

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the reviewer",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err, "an unknown skill is a tool error, not a turn failure")
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(
		t,
		toolResult.Content,
		`unknown skill "ghost-skill" in :ghost-skill; available skills: `,
	)
	assert.Contains(t, toolResult.Content, runtimeTestExplicitSkillName)
	assert.Len(t, driver.Requests(), 2, "the child never ran")
}

// allowed-tools removes workspace tools from a child, but never the messaging
// tools it needs to report back and to collect its own children's answers.
func TestLaunchAgentAllowedToolsExcludeWritesButKeepMessaging(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  "review the workspace",
			Agent: launchAgentChildName,
		},
		elelemtest.ToolCall(
			"call_restricted_write",
			toolNameWriteFile,
			`{"path":"must-not-exist.txt","content":"blocked"}`,
		),
		elelemtest.Text("review complete"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentRestrictedChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the reviewer",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	_, err = os.Stat(filepath.Join(fixture.workspace, "must-not-exist.txt"))
	assert.True(t, os.IsNotExist(err))

	childTools := toolNames(driver.requests(runtimeTestAgentName)[0].Tools)
	assert.NotContains(t, childTools, toolNameWriteFile)
	assert.Contains(t, childTools, toolNameReadFile)

	for _, messaging := range []string{
		toolNameSendMessage,
		toolNameWaitMessages,
		toolNameStopAgent,
		toolNameAsk,
		toolNameSearchExecutions,
		toolNameReadExecution,
		toolNameResumeExecution,
		toolNameResolveExecutionActivity,
	} {
		assert.Contains(t, childTools, messaging)
	}
}

func TestLaunchAgentAdHocDefinitionDeliversItsAnswer(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task: "say hi",
			AgentDefinition: &agentDefinitionInput{
				Name:         "scratch",
				Instructions: "do the ad-hoc task",
			},
		},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	turnEvents := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the ad-hoc child",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&turnEvents),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	data := agentEventData{}
	require.NoError(t, json.Unmarshal(
		onlyDeliveredNotice(t, turnEvents).Data,
		&data,
	))
	assert.Equal(t, "child says hi", data.Response)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	toolMessage := messages.Items[2]

	output := decodeLaunchAgentOutput(t, toolMessage.Content)
	assert.Equal(t, "scratch", output.Name)
	assert.Equal(t, AgentRunDefinitionAdHoc, output.Definition)
	assert.Equal(t, output.AgentRunID, data.RunID)
}

func TestLaunchAgentBothFormsIsValidationFailure(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: launchAgentChildName,
				AgentDefinition: &agentDefinitionInput{
					Name:         "scratch",
					Instructions: "do the task",
				},
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with both forms",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err, "a validation failure is a tool error, not a turn failure")
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "exactly one")
}

func TestLaunchAgentNeitherFormIsValidationFailure(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{Task: "say hi"}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with neither form",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "exactly one")
}

func TestLaunchAgentUnknownAgentListsEffectiveNames(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "say hi",
				Agent: "does-not-exist",
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch an unknown agent",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "not found")
	assert.Contains(t, toolResult.Content, launchAgentChildName)
}

const launchAgentCustomGeneralPurposeFile = "---\nname: general-purpose\ndescription: custom general purpose\n---\nCustom body."

// launchAgentToolResult runs one turn whose model launches the named agent
// and returns the launchAgent tool result. A launch expected to start a child
// waits for it. One expected to fail does not, because no child would ever
// answer.
func launchAgentToolResult(
	t *testing.T,
	agentName string,
	isLaunchExpected bool,
	options launchAgentFixtureOptions,
) toolResultPayload {
	t.Helper()

	input := launchAgentInput{Task: "say hi", Agent: agentName}

	var driver elelem.Driver = elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, input),
		),
		elelemtest.Text("done"),
	)
	if isLaunchExpected {
		driver = launchedChildDriver(t, input, elelemtest.Text("child says hi"))
	}

	fixture := newLaunchAgentFixture(t, driver, options)

	events := make([]Event, 0)
	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the agent",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))

	return toolResult
}

func TestLaunchAgentResolvesCoreAndCustomNames(t *testing.T) {
	testCases := []struct {
		name      string
		agent     string
		files     map[string]string
		wantAgent string
	}{
		{
			name:      "core name",
			agent:     "core:general-purpose",
			wantAgent: "core:general-purpose",
		},
		{
			name:      "plain name falls back to the core agent",
			agent:     "general-purpose",
			wantAgent: "core:general-purpose",
		},
		{
			name:      "plain name prefers the custom agent",
			agent:     "general-purpose",
			files:     map[string]string{"general-purpose": launchAgentCustomGeneralPurposeFile},
			wantAgent: "general-purpose",
		},
		{
			name:      "core name ignores the custom agent",
			agent:     "core:general-purpose",
			files:     map[string]string{"general-purpose": launchAgentCustomGeneralPurposeFile},
			wantAgent: "core:general-purpose",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := launchAgentToolResult(
				t,
				tc.agent,
				true,
				launchAgentFixtureOptions{AgentFiles: tc.files},
			)

			require.False(t, result.IsError, result.Content)
			assert.Equal(
				t,
				tc.wantAgent,
				decodeLaunchAgentOutput(t, result.Content).Name,
			)
		})
	}
}

// The child runs under the embedded agent's own instructions, and a custom
// agent of the same plain name changes nothing for the core: name.
func TestLaunchAgentCoreGeneralPurposeChildRunsOnTheEmbeddedInstructions(
	t *testing.T,
) {
	const embeddedBody = "Carry out the user's request within the resolved"

	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "say hi", Agent: "core:general-purpose"},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			"general-purpose": launchAgentCustomGeneralPurposeFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the agent",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)

	childSystem := childRequests[0].Messages[0].Text()
	assert.Contains(t, childSystem, embeddedBody)
	assert.NotContains(t, childSystem, "Custom body.")
	assert.Equal(
		t,
		[]string{taggedTask(t, "say hi")},
		tailTexts(childRequests[0].Messages, 1),
	)
}

func TestWorkspaceOnlyRootAgentStartsAndRunsATurn(t *testing.T) {
	const workspaceRoot = "workspace-root"

	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		Customize: func(options *RuntimeOptions) {
			options.RootAgent = workspaceRoot

			// Startup sees only the core agents and the config directory.
			resolver, err := harness.NewResolver(
				options.ConfigDirectory,
				harness.Limits{},
			)
			require.NoError(t, err)
			require.NoError(t, resolver.CheckRootAgent(workspaceRoot))
		},
	})

	writeRuntimeFile(
		t,
		filepath.Join(
			fixture.workspace,
			".agents",
			"agents",
			workspaceRoot+".md",
		),
		"---\nname: workspace-root\ndescription: workspace root\n---\nWorkspace root rules.",
	)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "hello",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)
	assert.Contains(
		t,
		driver.Requests()[0].Messages[0].Text(),
		"Workspace root rules.",
	)
}

func TestLaunchAgentRefusesADisabledCoreAgent(t *testing.T) {
	for _, agentName := range []string{
		"core:general-purpose",
		"general-purpose",
	} {
		t.Run(agentName, func(t *testing.T) {
			result := launchAgentToolResult(
				t,
				agentName,
				false,
				launchAgentFixtureOptions{
					Customize: func(options *RuntimeOptions) {
						resolver, err := harness.NewResolver(
							options.ConfigDirectory,
							harness.Limits{},
							harness.WithDisabledCoreAgents(
								[]string{"general-purpose"},
							),
						)
						require.NoError(t, err)

						options.Resolver = resolver
					},
				},
			)

			assert.True(t, result.IsError)
			assert.Contains(t, result.Content, "not found")
			assert.Contains(
				t,
				result.Content,
				"available agents: "+runtimeTestAgentName,
			)
		})
	}
}

func TestLaunchAgentOversizedAdHocInstructionsRejected(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task: "say hi",
				AgentDefinition: &agentDefinitionInput{
					Name:         "scratch",
					Instructions: strings.Repeat("x", 100),
				},
			}),
		),
		elelemtest.Text("done"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxAdHocInstructionBytes: 8},
	})

	events := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch with an oversized ad-hoc definition",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&events),
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	toolResult := toolResultPayload{}
	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, EventTypeToolResult),
		&toolResult,
	))
	assert.True(t, toolResult.IsError)
	assert.Contains(t, toolResult.Content, "too large")
}

// A parent launches a child, and that child itself launches a grandchild,
// proving launchAgent's recursion end to end. The grandchild's answer lands in
// the child's inbox as a tagged result, and the child's answer reaches the
// root as an event.
func TestLaunchAgentRecursion(t *testing.T) {
	driver := newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(launchAndWaitTurns(
			t,
			launchAgentCallID,
			launchAgentInput{Task: "delegate", Agent: launchAgentChildName},
			"parent final",
		)...),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(launchAndWaitTurns(
			t,
			launchAgentGrandCallID,
			launchAgentInput{
				Task:  "do the grandchild work",
				Agent: launchAgentGrandName,
			},
			"child final, saw: grandchild says hi",
		)...),
		launchAgentChildName: elelemtest.NewScriptedDriver(
			elelemtest.Text("grandchild says hi"),
		),
	}).
		after(runtimeTestAgentName, rootConversation, 2).
		after(launchAgentChildName, runtimeTestAgentName, 2)

	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
			launchAgentGrandName: launchAgentGrandAgentFile,
		},
	})

	turnEvents := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "start the chain",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&turnEvents),
	})
	require.NoError(t, err)
	assert.Equal(t, "parent final", result.Text)

	notice := onlyDeliveredNotice(t, turnEvents)
	data := agentEventData{}
	require.NoError(t, json.Unmarshal(notice.Data, &data))
	assert.Equal(t, "child final, saw: grandchild says hi", data.Response)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 2)

	byDepth := map[int]*AgentRun{}
	for _, run := range runs {
		byDepth[run.Depth] = run
	}

	require.Contains(t, byDepth, 1)
	require.Contains(t, byDepth, 2)
	assert.Equal(t, launchAgentChildName, byDepth[1].Name)
	assert.Equal(t, launchAgentGrandName, byDepth[2].Name)
	assert.Equal(t, AgentRunStateCompleted, byDepth[1].Snapshot().State)
	assert.Equal(t, AgentRunStateCompleted, byDepth[2].Snapshot().State)
	assert.Nil(t, byDepth[1].ParentAgentRunID)
	require.NotNil(t, byDepth[2].ParentAgentRunID)
	assert.Equal(t, byDepth[1].ID, *byDepth[2].ParentAgentRunID)
	assert.Equal(t, launchAgentCallID, byDepth[1].ParentToolCallID)
	assert.Equal(t, launchAgentGrandCallID, byDepth[2].ParentToolCallID)

	query := repositories.Use(fixture.handle.GormDB)
	storedChild, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(byDepth[1].ID)).
		First()
	require.NoError(t, err)
	assert.Nil(t, storedChild.ParentAgentRunID)
	assert.Equal(t, launchAgentCallID, storedChild.ParentToolCallID)

	storedGrandchild, err := query.AgentRun.WithContext(context.Background()).
		Where(query.AgentRun.ID.Eq(byDepth[2].ID)).
		First()
	require.NoError(t, err)
	require.NotNil(t, storedGrandchild.ParentAgentRunID)
	assert.Equal(t, byDepth[1].ID, *storedGrandchild.ParentAgentRunID)
	assert.Equal(t, launchAgentGrandCallID, storedGrandchild.ParentToolCallID)

	grandchildResult, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: launchAgentGrandName,
		Run:  byDepth[2].ID.String(),
		Kind: messageKindResult,
		Text: "grandchild says hi",
	})
	require.NoError(t, err)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 3)
	assert.Equal(
		t,
		[]string{grandchildResult},
		tailTexts(childRequests[2].Messages, 1),
		"the grandchild's answer reaches the child after its wait",
	)

	grandchildRequests := driver.requests(launchAgentChildName)
	require.Len(t, grandchildRequests, 1)

	grandchildTask, err := renderAgentMessage(agentMessage{
		From: messageSenderAgent,
		Name: launchAgentChildName,
		Run:  byDepth[1].ID.String(),
		Text: "do the grandchild work",
	})
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{grandchildTask},
		tailTexts(grandchildRequests[0].Messages, 1),
		"the grandchild's task names the child as its sender",
	)

	childTranscript, err := fixture.store.ListAgentRunMessages(
		context.Background(),
		result.SessionID,
		byDepth[1].ID,
		session.ListAgentRunMessagesOptions{Limit: session.MaximumPageLimit},
	)
	require.NoError(t, err)

	transcriptTexts := make([]string, 0, len(childTranscript.Items))
	for _, item := range childTranscript.Items {
		transcriptTexts = append(transcriptTexts, item.Content)
	}

	assert.Contains(
		t,
		transcriptTexts,
		grandchildResult,
		"the delivered result is part of the child's durable transcript",
	)
}

// A child at the configured limit cannot request more child work because the
// model never receives the launchAgent tool definition.
func TestLaunchAgentAtDepthLimitDoesNotExposeLaunchAgent(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "delegate", Agent: launchAgentChildName},
		elelemtest.Text("child final"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxDepth: 1},
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentDepthLimitedChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "start the chain",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", result.Text)

	rootRequests := driver.requests(rootConversation)
	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.Contains(t, toolNames(rootRequests[0].Tools), toolNameLaunchAgent)
	assert.Contains(t, toolNames(childRequests[0].Tools), toolNameReadFile)
	assert.NotContains(
		t,
		toolNames(childRequests[0].Tools),
		toolNameLaunchAgent,
	)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(
		t,
		runs,
		1,
		"the depth-limited child must be the only registered run",
	)
	assert.Equal(t, launchAgentChildName, runs[0].Name)
	assert.Equal(t, AgentRunStateCompleted, runs[0].Snapshot().State)
}

// The tool is absent at the depth limit, but a stale provider response can
// still contain an old tool call. The handler itself must reject that call
// before it creates another child run.
func TestLaunchAgentRejectsUnadvertisedCallPastDepthLimit(t *testing.T) {
	fixture := newLaunchAgentFixture(
		t,
		elelemtest.NewScriptedDriver(),
		launchAgentFixtureOptions{
			AgentLimits: AgentRunLimits{MaxDepth: 1},
			AgentFiles: map[string]string{
				launchAgentChildName: launchAgentChildAgentFile,
			},
		},
	)

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	_, err = fixture.runtime.launchAgent(
		contextWithAgentDepth(
			context.Background(),
			fixture.runtime.agentLimits.MaxDepth,
		),
		&launchAgentDeps{
			snapshot:  snapshot,
			sessionID: uuid.New(),
		},
		launchAgentInput{
			Task:  "delegate",
			Agent: launchAgentChildName,
		},
	)
	require.ErrorIs(t, err, ErrAgentDepthExceeded)

	query := repositories.Use(fixture.handle.GormDB)
	agentRunCount, err := query.AgentRun.WithContext(context.Background()).Count()
	require.NoError(t, err)
	assert.Zero(t, agentRunCount)
}

func toolNames(tools []elelem.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}

	return names
}

// A low MaxChildTurns bound must withhold tools on the child's final round,
// proving the bound is threaded into the child's own request rather than
// inherited from the parent turn's larger MaxToolRounds.
func TestLaunchAgentMaxChildTurnsWithholdsToolsOnLastRound(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{Task: "say hi", Agent: launchAgentChildName},
		elelemtest.Text("child says hi"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentLimits: AgentRunLimits{MaxChildTurns: 1},
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	_, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	childRequests := driver.requests(runtimeTestAgentName)
	require.Len(t, childRequests, 1)
	assert.Empty(
		t,
		childRequests[0].Tools,
		"max child turns of 1 must withhold tools on the only round",
	)

	parentFirstRequest := driver.requests(rootConversation)[0]
	assert.NotEmpty(
		t,
		parentFirstRequest.Tools,
		"the parent's own round must still offer its full tool set",
	)
}

// A child's own tool call can fail without failing the child's run: the
// child sees the failure as an ordinary tool result and still returns a
// final answer.
func TestLaunchAgentChildToolFailureStaysVisibleAndRunCompletes(t *testing.T) {
	driver := launchedChildDriver(
		t,
		launchAgentInput{
			Task:  "read a missing file",
			Agent: launchAgentChildName,
		},
		elelemtest.ToolCall(
			"call_read",
			toolNameReadFile,
			runtimeToolArguments(t, "absent.txt"),
		),
		elelemtest.Text("the file is missing"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	turnEvents := make([]Event, 0)
	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
		OnEvent:   collectEvents(&turnEvents),
	})
	require.NoError(t, err, "a child tool failure must not fail the parent turn")
	assert.Equal(t, "done", result.Text)

	data := agentEventData{}
	require.NoError(t, json.Unmarshal(
		onlyDeliveredNotice(t, turnEvents).Data,
		&data,
	))
	assert.Equal(t, "the file is missing", data.Response)

	registry, err := fixture.runtime.sessionAgentRuns(result.SessionID)
	require.NoError(t, err)

	runs := registry.List()
	require.Len(t, runs, 1)
	assert.Equal(
		t,
		AgentRunStateCompleted,
		runs[0].Snapshot().State,
		"a failed child tool call must not mark the run itself failed",
	)

	runEvents, _, _ := runs[0].ReadEvents(0, 0)
	found := false

	for _, event := range runEvents {
		if event.Type != EventTypeAgentRunToolResult {
			continue
		}

		payload := toolResultPayload{}
		require.NoError(t, json.Unmarshal(event.Payload, &payload))

		if payload.IsError {
			found = true
		}
	}

	assert.True(
		t,
		found,
		"the run's own event buffer must record the failed tool call",
	)
}

// A caller cannot switch the startup workspace session by supplying a session
// ID. Child runs remain available to every later turn in that workspace.
func TestLaunchAgentRunRemainsInStartupWorkspaceSession(t *testing.T) {
	rootTurns := launchAndWaitTurns(
		t,
		launchAgentCallID,
		launchAgentInput{Task: "say hi", Agent: launchAgentChildName},
		"done",
	)
	driver := newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(append(
			rootTurns,
			elelemtest.Text("unrelated session reply"),
		)...),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(
			elelemtest.Text("child says hi"),
		),
	}).after(runtimeTestAgentName, rootConversation, 2)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	first, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch the child",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	registryA, err := fixture.runtime.sessionAgentRuns(first.SessionID)
	require.NoError(t, err)

	runs := registryA.List()
	require.Len(t, runs, 1)

	second, err := fixture.runtime.Run(context.Background(), TurnRequest{
		SessionID: &first.SessionID,
		Message:   "an unrelated session",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	require.Equal(t, first.SessionID, second.SessionID)

	registryB, err := fixture.runtime.sessionAgentRuns(second.SessionID)
	require.NoError(t, err)

	got, found := registryB.Get(runs[0].ID)
	assert.True(
		t,
		found,
		"a child run from the startup workspace must remain visible",
	)
	assert.Equal(t, runs[0].ID, got.ID)
}

// finishAgentRun records a cancellation durably and reports it as the
// outcome, whatever wrapped the cancellation.
func TestFinishAgentRunRecordsACancellation(t *testing.T) {
	driver := elelemtest.NewScriptedDriver()
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{})

	registry, run, _, sink := startDurableAgentRun(
		context.Background(),
		t,
		fixture,
	)

	result := fixture.runtime.finishAgentRun(
		context.Background(),
		registry,
		sink,
		run,
		nil,
		&wrappedCancelError{},
	)
	assert.Equal(t, AgentRunStateCancelled, result.State)
	assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)

	stored, err := fixture.store.GetAgentRun(
		context.Background(),
		run.SessionID,
		run.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateCancelled, stored.State)
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

// A child keeps running after the turn that launched it ends. When it
// finishes, its agent.finished event wakes the idle root, and the woken turn
// reads the answer.
func TestLaunchAgentChildOutlivesItsTurnAndWakesTheRoot(t *testing.T) {
	driver := newConversationDriver(map[string]*elelemtest.ScriptedDriver{
		rootConversation: elelemtest.NewScriptedDriver(
			elelemtest.ToolCall(
				launchAgentCallID,
				toolNameLaunchAgent,
				launchAgentArguments(t, launchAgentInput{
					Task:  "say hi later",
					Agent: launchAgentChildName,
				}),
			),
			elelemtest.Text("launched"),
			elelemtest.Text("woken and read the answer"),
		),
		runtimeTestAgentName: elelemtest.NewScriptedDriver(
			elelemtest.Text("late hi"),
		),
	}).after(runtimeTestAgentName, rootConversation, 2)

	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch and leave",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "launched", result.Text)

	run := onlyRun(t, fixture, func() uuid.UUID { return result.SessionID })

	select {
	case <-run.Done():
	case <-time.After(launchAgentShutdownBound):
		require.FailNow(t, "the child never finished")
	}

	assert.Equal(t, AgentRunStateCompleted, run.Snapshot().State)

	// Turns list newest first. The woken turn is the second one.
	require.Eventually(t, func() bool {
		turns, err := fixture.store.ListTurns(
			context.Background(),
			result.SessionID,
			session.ListTurnsOptions{},
		)
		if err != nil || len(turns.Items) != 2 {
			return false
		}

		return turns.Items[0].State == models.TurnStateCompleted
	}, launchAgentShutdownBound, launchAgentPollInterval)

	rootRequests := driver.requests(rootConversation)
	require.Len(t, rootRequests, 3)

	wokenConversation := conversationText(rootRequests[2:])
	assert.Contains(t, wokenConversation, agentWakeInstructions)
	assert.Contains(
		t,
		wokenConversation,
		`"response":"late hi"`,
		"the woken turn reads the child's answer from the event",
	)
}

// Cancelling a turn stops the children it started, and the cancelled child's
// report waits rather than waking the root again.
func TestCancelledTurnCancelsTheChildrenItStarted(t *testing.T) {
	driver := heldChildDriver(
		t,
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "hold on",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.ToolCall(
			launchAgentWaitCallID,
			toolNameWaitMessages,
			`{}`,
		),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	turnDone := make(chan error, 1)

	go func() {
		_, err := fixture.runtime.Run(context.Background(), TurnRequest{
			Message:   "launch and wait",
			Workspace: fixture.workspace,
		})
		turnDone <- err
	}()

	run := onlyRun(t, fixture, fixture.runtime.SessionID)

	cancelled, err := fixture.store.Cancel(context.Background(), run.SessionID)
	require.NoError(t, err)
	assert.True(t, cancelled)

	select {
	case err := <-turnDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(launchAgentShutdownBound):
		require.FailNow(t, "the cancelled turn never returned")
	}

	select {
	case <-run.Done():
	case <-time.After(launchAgentShutdownBound):
		require.FailNow(t, "the child outlived its cancelled turn")
	}

	assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)

	// Finish closes Done before it publishes, so the report may trail it.
	require.Eventually(t, func() bool {
		return fixture.eventBus.Pending(run.SessionID) == 1
	}, launchAgentShutdownBound, launchAgentPollInterval)

	pending := fixture.eventBus.Peek(run.SessionID)
	assert.Equal(t, agentFailedEventType, pending.Notices[0].Type)
	assert.Equal(t, events.DeliveryQueue, pending.Notices[0].Delivery)
}

// Cancelling the session stops children that outlived their turn, and says a
// cancellation was requested even though no turn is running.
func TestCancelSessionStopsChildrenOfEndedTurns(t *testing.T) {
	driver := heldChildDriver(
		t,
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "hold on",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Text("launched"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "launch and leave",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	run := onlyRun(t, fixture, func() uuid.UUID { return result.SessionID })
	assert.Equal(t, AgentRunStateRunning, run.Snapshot().State)

	response, err := fixture.runtime.CancelSession(
		context.Background(),
		result.SessionID,
	)
	require.NoError(t, err)
	assert.True(t, response.CancelRequested)

	select {
	case <-run.Done():
	case <-time.After(launchAgentShutdownBound):
		require.FailNow(t, "the child was never cancelled")
	}

	assert.Equal(t, AgentRunStateCancelled, run.Snapshot().State)
}

func startDurableAgentRun(
	parentCtx context.Context,
	t *testing.T,
	fixture runtimeFixture,
) (*AgentRunRegistry, *AgentRun, context.Context, *agentRunSink) {
	t.Helper()

	sessionID := uuid.New()
	_, err := fixture.store.CreateOrResume(
		context.Background(),
		&sessionID,
		session.OpenSessionOptions{
			RootAgent: runtimeTestAgentName,
			ModelID:   runtimeTestModelReference,
		},
	)
	require.NoError(t, err)

	lease, err := fixture.store.AcquireTurn(
		context.Background(),
		sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: fixture.workspace,
			Messages: []session.MessageInput{{
				Role:    models.MessageRoleUser,
				Content: "start child",
			}},
		},
	)
	require.NoError(t, err)

	registry, err := fixture.runtime.sessionAgentRuns(sessionID)
	require.NoError(t, err)

	run, runCtx, err := registry.Start(parentCtx, StartAgentRunInput{
		ID:           uuid.New(),
		ParentTurnID: lease.TurnID,
		RequestID:    uuid.New(),
		Name:         "child",
		Definition:   AgentRunDefinitionStored,
		Depth:        1,
	}, func(run *AgentRun) error {
		_, createErr := fixture.store.CreateAgentRun(
			context.Background(),
			sessionID,
			session.StartAgentRunInput{
				ID:               run.ID,
				ParentTurnID:     run.ParentTurnID,
				ParentToolCallID: run.ParentToolCallID,
				RequestID:        run.RequestID,
				Name:             run.Name,
				Definition:       models.AgentRunDefinitionStored,
				Depth:            int64(run.Depth),
				Workspace:        fixture.workspace,
				ModelReference:   runtimeTestModelReference,
				ModelID:          runtimeTestModelID,
				Task:             "child task",
				Instructions:     "child instructions",
				AllowedToolsJSON: "[]",
				SystemPrompt:     "child system prompt",
				StartedAt:        run.StartedAt,
			},
		)

		return createErr
	})
	require.NoError(t, err)

	return registry, run, runCtx, newAgentRunSink(
		fixture.store,
		sessionID,
		run,
		nil,
		runtimeTestModelReference,
	)
}

// wrappedCancelError satisfies errors.Is(err, context.Canceled) without needing a
// real cancelled context.Context to construct, since finishAgentRun's
// decision reads parentCtx.Err() separately from the error value itself.
type wrappedCancelError struct{}

func (w *wrappedCancelError) Error() string { return context.Canceled.Error() }

func (w *wrappedCancelError) Is(target error) bool { return target == context.Canceled }
