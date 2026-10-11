package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/config"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	skillTestAlpha          = "skill-alpha"
	skillTestBravo          = "skill-bravo"
	skillTestAlphaBody      = "Alpha procedure, then :skill-bravo."
	skillTestBravoBody      = "Bravo procedure."
	skillTestCycleBravoBody = "Bravo procedure, back to :skill-alpha."
	skillTestMessageFormat  = "<skill>\n<name>%s</name>\n" +
		"<description>%s</description>\n<directory>%s</directory>\n\n%s\n</skill>"
	skillTestCoreMessage = "Plan this with :core:planning please."
	skillTestTypedAlpha  = "Run :skill-alpha now."
	skillTestFixedHour   = 9
	skillTestFileLimit   = 400
	skillTestBigBody     = 300

	// skillTestCompactionContextTokens lets the transcript grow to eight
	// messages at compactionTestTokensPerMessage each before it compacts.
	skillTestCompactionContextTokens = 800

	// skillTestCompactedTurns is how many turns run before the skill is named
	// again: enough that a compaction has reached the skill message.
	skillTestCompactedTurns = 6
)

// writeSkillFile writes a workspace skill whose description is its name plus
// " skill", so a test can predict the catalogue line.
func writeSkillFile(
	t *testing.T,
	fixture runtimeFixture,
	name string,
	body string,
) {
	t.Helper()

	writeRuntimeFile(
		t,
		filepath.Join(fixture.workspace, ".agents", "skills", name, "SKILL.md"),
		"---\nname: "+name+"\ndescription: "+name+" skill\n---\n"+body,
	)
}

// expectedSkillMessage renders the message a skill must reach the model as,
// from the skill the harness resolves for name.
func expectedSkillMessage(
	t *testing.T,
	fixture runtimeFixture,
	name string,
) string {
	t.Helper()

	snapshot, err := fixture.runtime.resolver.Resolve(fixture.workspace)
	require.NoError(t, err)

	activated, err := snapshot.ActivateSkill(name)
	require.NoError(t, err)

	return fmt.Sprintf(
		skillTestMessageFormat,
		activated.Name,
		activated.Description,
		activated.Directory,
		activated.Content,
	)
}

func decodeEventInto(
	t *testing.T,
	events []Event,
	eventType string,
	target any,
) {
	t.Helper()

	require.NoError(t, json.Unmarshal(
		decodeEventPayload(t, events, eventType),
		target,
	))
}

func fixedClock(fixture runtimeFixture) {
	fixture.runtime.now = func() time.Time {
		return time.Date(2026, time.October, 10, skillTestFixedHour, 0, 0, 0, time.UTC)
	}
}

func runSkillTurn(
	t *testing.T,
	fixture runtimeFixture,
	message string,
	events *[]Event,
) *TurnResult {
	t.Helper()

	request := TurnRequest{Message: message, Workspace: fixture.workspace}
	if events != nil {
		request.OnEvent = collectEvents(events)
	}

	result, err := fixture.runtime.Run(context.Background(), request)
	require.NoError(t, err)

	return result
}

// tailTexts returns the texts of the last count messages of a request.
func tailTexts(messages []elelem.Message, count int) []string {
	return messageTexts(messages[len(messages)-count:])
}

func TestRuntimeSkillReferenceAddsAUserSkillMessageAfterTheUserMessage(
	t *testing.T,
) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("done"),
		elelemtest.Text("again"),
	)
	fixture := newRuntimeFixture(t, driver)
	fixedClock(fixture)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	runSkillTurn(t, fixture, runtimeTestExplicitSkillMessage, nil)
	runSkillTurn(t, fixture, "Apply review-rules to this task.", nil)

	requests := driver.Requests()
	require.Len(t, requests, 2)

	first := requests[0].Messages
	assert.Equal(
		t,
		[]string{
			runtimeTestExplicitSkillMessage,
			expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
		},
		tailTexts(first, 2),
	)
	assert.Equal(t, elelem.RoleUser, first[len(first)-1].Role)
	assert.NotContains(t, first[0].Text(), skillTestBravoBody)
	assert.Equal(t, first[0].Text(), requests[1].Messages[0].Text())
}

func TestRuntimeCoreSkillReferenceIsNamedWithTheCorePrefix(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)

	runSkillTurn(t, fixture, skillTestCoreMessage, nil)

	messages := driver.Requests()[0].Messages
	assert.Equal(t, skillTestCoreMessage, messages[len(messages)-2].Text())
	assert.Equal(
		t,
		expectedSkillMessage(t, fixture, harness.CorePrefix+"planning"),
		messages[len(messages)-1].Text(),
	)
	assert.True(t, strings.HasPrefix(
		messages[len(messages)-1].Text(),
		"<skill>\n<name>core:planning</name>\n",
	))
}

func TestRuntimeStoresTheTypedMessageThenInjectedSkillRows(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	result := runSkillTurn(t, fixture, runtimeTestExplicitSkillMessage, nil)

	stored, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	require.Len(t, stored.Items, 3)

	assert.Equal(
		t,
		[]string{
			runtimeTestExplicitSkillMessage,
			expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
			"done",
		},
		messageContents(stored.Items),
	)
	assert.Equal(
		t,
		[]models.MessageRole{
			models.MessageRoleUser,
			models.MessageRoleUser,
			models.MessageRoleAssistant,
		},
		messageRoles(stored.Items),
	)
	assert.False(t, stored.Items[0].Injected)
	assert.Equal(t, models.InjectedKindNone, stored.Items[0].InjectedKind)
	assert.True(t, stored.Items[1].Injected)
	assert.Equal(t, models.InjectedKindSkill, stored.Items[1].InjectedKind)
}

func TestRuntimeReplaysStoredSkillMessagesInLaterTurns(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.Text("second reply"),
	)
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	runSkillTurn(t, fixture, runtimeTestExplicitSkillMessage, nil)
	runSkillTurn(t, fixture, "carry on", nil)

	second := driver.Requests()[1].Messages
	assert.Equal(
		t,
		[]string{
			runtimeTestExplicitSkillMessage,
			expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
			"first reply",
			"carry on",
		},
		tailTexts(second, 4),
	)
}

// useSummarizeCompaction runs the fixture in summarize mode, where a stored
// compaction is the only thing that takes a delivered skill from the model.
func useSummarizeCompaction(options *RuntimeOptions) {
	options.CompactionMode = config.CompactionModeSummarize
}

// Under drop-oldest the request window silently loses old messages and Peen
// stores no compaction, so a skill from an earlier turn may be gone from the
// model's view. Naming it in a later turn delivers it in full.
func TestRuntimeDeliversASkillInFullInALaterTurnUnderDropOldest(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.Text("second reply"),
	)
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) {
			options.CompactionMode = config.CompactionModeDropOldest
		},
	)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	runSkillTurn(t, fixture, runtimeTestExplicitSkillMessage, nil)
	runSkillTurn(t, fixture, "Again :review-rules please.", nil)

	second := driver.Requests()[1].Messages
	assert.Equal(
		t,
		[]string{
			"Again :review-rules please.",
			expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
		},
		tailTexts(second, 2),
	)
}

// A skill the model loaded with useSkill in an earlier turn is no more
// certain to be in the window under drop-oldest than one named with :name.
func TestRuntimeDeliversAUseSkillSkillInFullInALaterTurnUnderDropOldest(
	t *testing.T,
) {
	steps := useSkillScript(t, skillTestAlpha, "first reply")
	steps = append(steps, elelemtest.Text("second reply"))

	driver := elelemtest.NewScriptedDriver(steps...)
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, skillTestAlpha, skillTestAlphaBody)
	writeSkillFile(t, fixture, skillTestBravo, skillTestBravoBody)

	runSkillTurn(t, fixture, "load the procedure", nil)
	runSkillTurn(t, fixture, skillTestTypedAlpha, nil)

	requests := driver.Requests()
	require.Len(t, requests, 3)
	assert.Equal(
		t,
		[]string{
			skillTestTypedAlpha,
			expectedSkillMessage(t, fixture, skillTestAlpha),
			expectedSkillMessage(t, fixture, skillTestBravo),
		},
		tailTexts(requests[2].Messages, 3),
	)
}

func TestRuntimeNotesASkillAlreadyLoadedEarlierInSummarizeMode(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.Text("second reply"),
	)
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		useSummarizeCompaction,
	)
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	runSkillTurn(t, fixture, runtimeTestExplicitSkillMessage, nil)
	result := runSkillTurn(t, fixture, "Again :review-rules please.", nil)

	second := driver.Requests()[1].Messages
	assert.Equal(
		t,
		[]string{
			"Again :review-rules please.",
			"<note>Skill review-rules is already loaded earlier in this " +
				"conversation.</note>",
		},
		tailTexts(second, 2),
	)

	skillCopies := 0

	for _, text := range messageTexts(second) {
		if strings.HasPrefix(text, "<skill>") {
			skillCopies++
		}
	}

	assert.Equal(t, 1, skillCopies)

	stored, err := fixture.store.ListMessages(
		context.Background(),
		result.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)

	note := stored.Items[len(stored.Items)-2]
	assert.True(t, note.Injected)
	assert.Equal(t, models.InjectedKindSkill, note.InjectedKind)
}

func TestRuntimeDeliversReferencedSkillsAfterTheSkillThatNamedThem(
	t *testing.T,
) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, skillTestAlpha, skillTestAlphaBody)
	writeSkillFile(t, fixture, skillTestBravo, skillTestBravoBody)

	runSkillTurn(t, fixture, skillTestTypedAlpha, nil)

	assert.Equal(
		t,
		[]string{
			skillTestTypedAlpha,
			expectedSkillMessage(t, fixture, skillTestAlpha),
			expectedSkillMessage(t, fixture, skillTestBravo),
		},
		tailTexts(driver.Requests()[0].Messages, 3),
	)
}

func TestRuntimeDeliversEachSkillOnceWhenSkillsReferenceEachOther(
	t *testing.T,
) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, skillTestAlpha, skillTestAlphaBody)
	writeSkillFile(t, fixture, skillTestBravo, skillTestCycleBravoBody)

	runSkillTurn(t, fixture, skillTestTypedAlpha, nil)

	messages := driver.Requests()[0].Messages
	assert.Equal(
		t,
		[]string{
			skillTestTypedAlpha,
			expectedSkillMessage(t, fixture, skillTestAlpha),
			expectedSkillMessage(t, fixture, skillTestBravo),
		},
		tailTexts(messages, 3),
	)
	assert.Len(t, messages, 4)
}

func TestRuntimeRejectsAnUnknownSkillAsAValidationFailure(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("must not run"))
	fixture := newRuntimeFixture(t, driver)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   ":missing-skill inspect the project",
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, harness.ErrSkillNotFound)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Nil(t, result)
	assert.Empty(t, driver.Requests())
}

func TestRuntimeRejectsSkillContentOverTheHarnessLimit(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("must not run"))
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) {
			resolver, err := harness.NewResolver(
				options.ConfigDirectory,
				harness.Limits{MaxFileBytes: skillTestFileLimit},
			)
			require.NoError(t, err)

			options.Resolver = resolver
		},
	)
	big := strings.Repeat("x", skillTestBigBody)
	writeSkillFile(t, fixture, skillTestAlpha, "needs :skill-bravo "+big)
	writeSkillFile(t, fixture, skillTestBravo, big)

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   skillTestTypedAlpha,
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, harness.ErrSkillContentTooLarge)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Nil(t, result)
	assert.Empty(t, driver.Requests())
}

func TestRuntimeEmitsASkillEventForEachSkillMessageAtTurnStart(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, skillTestAlpha, skillTestAlphaBody)
	writeSkillFile(t, fixture, skillTestBravo, skillTestBravoBody)

	events := make([]Event, 0)
	runSkillTurn(t, fixture, skillTestTypedAlpha, &events)

	assert.Equal(
		t,
		[]string{
			EventTypeUserMessageCreated,
			EventTypeSkillInjected,
			EventTypeSkillInjected,
			eventTypeTurnStarted,
			eventTypeTurnCompleted,
		},
		harnessEventTypes(events),
	)

	payload := skillInjectedPayload{}
	decodeEventInto(t, events, EventTypeSkillInjected, &payload)
	assert.Equal(t, skillTestAlpha, payload.Name)
	assert.Equal(
		t,
		expectedSkillMessage(t, fixture, skillTestAlpha),
		payload.Content,
	)
}

// useSkillScript scripts one turn that loads name with useSkill, then
// replies with reply.
func useSkillScript(t *testing.T, name, reply string) []elelemtest.Turn {
	t.Helper()

	arguments, err := json.Marshal(useSkillInput{Name: name})
	require.NoError(t, err)

	return []elelemtest.Turn{
		elelemtest.ToolCall(skillTestCallID, toolNameUseSkill, string(arguments)),
		elelemtest.Text(reply),
	}
}

func TestRuntimeNotesASkillLoadedWithUseSkillInAnEarlierTurnInSummarizeMode(
	t *testing.T,
) {
	testCases := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "the skill the model loaded",
			message: "Run :skill-alpha now.",
			want: "<note>Skill skill-alpha is already loaded earlier in " +
				"this conversation.</note>",
		},
		{
			name:    "a skill that came along as a reference",
			message: "Run :skill-bravo now.",
			want: "<note>Skill skill-bravo is already loaded earlier in " +
				"this conversation.</note>",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			steps := useSkillScript(t, skillTestAlpha, "first reply")
			steps = append(steps, elelemtest.Text("second reply"))

			driver := elelemtest.NewScriptedDriver(steps...)
			fixture := newRuntimeFixtureWithOptions(
				t,
				driver,
				useSummarizeCompaction,
			)
			writeSkillFile(t, fixture, skillTestAlpha, skillTestAlphaBody)
			writeSkillFile(t, fixture, skillTestBravo, skillTestBravoBody)

			runSkillTurn(t, fixture, "load the procedure", nil)
			runSkillTurn(t, fixture, tc.message, nil)

			requests := driver.Requests()
			require.Len(t, requests, 3)

			last := requests[2].Messages
			assert.Equal(t, []string{tc.message, tc.want}, tailTexts(last, 2))

			for _, text := range messageTexts(last) {
				assert.False(t, strings.HasPrefix(text, "<skill>"))
			}
		})
	}
}

func TestDeliveredSkillNames(t *testing.T) {
	t.Parallel()

	const (
		useSkillCalls = `[{"ID":"call-1","Name":"useSkill","Arguments":{}}]`
		otherCalls    = `[{"ID":"call-1","Name":"readFile","Arguments":{}}]`
		loaded        = `{"name":"alpha","referencedSkills":` +
			`[{"name":"bravo"},{"name":"charlie"}]}`
	)

	assistant := func(calls string) *models.Message {
		return &models.Message{
			Role:          models.MessageRoleAssistant,
			ToolCallsJSON: calls,
		}
	}
	result := func(callID, content string, isError bool) *models.Message {
		return &models.Message{
			Role:       models.MessageRoleTool,
			ToolCallID: callID,
			Content:    content,
			IsError:    isError,
		}
	}

	testCases := []struct {
		name    string
		history []*models.Message
		want    []string
	}{
		{name: "empty history", history: nil, want: []string{}},
		{
			name: "useSkill result counts the skill and its references",
			history: []*models.Message{
				assistant(useSkillCalls),
				result("call-1", loaded, false),
			},
			want: []string{"alpha", "bravo", "charlie"},
		},
		{
			name: "useSkill error result counts nothing",
			history: []*models.Message{
				assistant(useSkillCalls),
				result("call-1", loaded, true),
			},
			want: []string{},
		},
		{
			name: "another tool's result counts nothing",
			history: []*models.Message{
				assistant(otherCalls),
				result("call-1", loaded, false),
			},
			want: []string{},
		},
		{
			name: "a result for an unknown call counts nothing",
			history: []*models.Message{
				assistant(useSkillCalls),
				result("call-9", loaded, false),
			},
			want: []string{},
		},
		{
			name: "an unparsable result counts nothing",
			history: []*models.Message{
				assistant(useSkillCalls),
				result("call-1", "not json", false),
			},
			want: []string{},
		},
		{
			name: "unparsable stored tool calls count nothing",
			history: []*models.Message{
				assistant("not json"),
				result("call-1", loaded, false),
			},
			want: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, deliveredSkillNames(tc.history))
		})
	}
}

func TestRuntimeSystemPromptCataloguesCoreSkillsBeforeCustomOnes(
	t *testing.T,
) {
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("done"))
	fixture := newRuntimeFixture(t, driver)
	writeSkillFile(t, fixture, skillTestAlpha, skillTestAlphaBody)

	runSkillTurn(t, fixture, "carry on", nil)

	system := driver.Requests()[0].Messages[0].Text()
	freshness := strings.Index(system, "core:freshness: ")
	planning := strings.Index(system, "core:planning: ")
	custom := strings.Index(system, skillTestAlpha+": ")

	require.NotEqual(t, -1, freshness)
	require.NotEqual(t, -1, planning)
	require.NotEqual(t, -1, custom)
	assert.Less(t, freshness, planning)
	assert.Less(t, planning, custom)
}

// A compacted skill message no longer reaches the model, so naming the skill
// again after the compaction sends it in full rather than as a note.
func TestRuntimeDeliversASkillAgainAfterItWasCompacted(t *testing.T) {
	const skillRow = 3

	driver := elelemtest.NewScriptedDriver(
		compactionScriptedTurns(skillTestCompactedTurns + 1)...,
	).WithTokenCounter(compactionTokenCounter{
		perMessage: compactionTestTokensPerMessage,
	})
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) {
			options.CompactionMode = config.CompactionModeSummarize
			options.MaxContextTokens = skillTestCompactionContextTokens
			options.CompactionOutputTokens = compactionTestOutputTokens
		},
	)
	summarizer := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = summarizer.summarize
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	first := runCompactionTurn(t, fixture, nil, compactionUserText(1))
	sessionID := first.SessionID

	runCompactionTurn(
		t,
		fixture,
		&sessionID,
		runtimeTestExplicitSkillMessage,
	)

	for turn := 3; turn <= skillTestCompactedTurns; turn++ {
		runCompactionTurn(t, fixture, &sessionID, compactionUserText(turn))
	}

	rows := orderedMessages(t, fixture, sessionID)
	require.Equal(t, models.InjectedKindSkill, rows[skillRow].InjectedKind)
	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	require.NotNil(t, compaction)
	require.GreaterOrEqual(
		t,
		compaction.ToSequence,
		rows[skillRow].Sequence,
		"the skill message must be inside the compacted prefix",
	)

	again := "Again :review-rules please."
	runCompactionTurn(t, fixture, &sessionID, again)

	requests := driver.Requests()
	last := requests[len(requests)-1].Messages
	assert.Equal(
		t,
		[]string{
			again,
			expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
		},
		tailTexts(last, 2),
	)
}

// A compaction inside a turn summarizes the skill message away, so a :name
// queued after it gets the skill in full, not the note that it is already
// loaded. The turn's first request fits the budget and its second does not,
// so the compaction happens mid-turn, and the message is queued right after
// it.
func TestRuntimeDeliversASkillAgainAfterAMidTurnCompaction(t *testing.T) {
	const (
		// The budget holds eleven messages at 100 tokens each, with the
		// summary allowance left over.
		budgetTokens     = 1120
		allowanceTokens  = 10
		ordinaryTurns    = 3
		queuedSkillAgain = "Once more, :review-rules please."
		midTurnMessage   = "look around"
	)

	scripted := compactionScriptedTurns(ordinaryTurns + 1)
	scripted = append(
		scripted,
		elelemtest.ToolCall(
			runtimeToolCallID,
			toolNameListFiles,
			`{"path":"."}`,
		),
		elelemtest.Text("looked"),
		elelemtest.Text("handled"),
	)

	driver := elelemtest.NewScriptedDriver(scripted...).
		WithTokenCounter(compactionTokenCounter{
			perMessage: compactionTestTokensPerMessage,
		})
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) {
			options.CompactionMode = config.CompactionModeSummarize
			options.MaxContextTokens = budgetTokens
			options.CompactionOutputTokens = allowanceTokens
		},
	)
	summarizer := newCompactionSummarizeStub(compactionScenarioSummaryText)
	fixture.runtime.compactionOptions.Summarize = summarizer.summarize
	writeSkillFile(t, fixture, runtimeTestExplicitSkillName, skillTestBravoBody)

	first := runCompactionTurn(
		t,
		fixture,
		nil,
		runtimeTestExplicitSkillMessage,
	)
	sessionID := first.SessionID

	for turn := 2; turn <= ordinaryTurns+1; turn++ {
		runCompactionTurn(t, fixture, &sessionID, compactionUserText(turn))
	}

	before, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	require.Nil(t, before, "nothing may compact before the mid-turn request")

	var (
		queued    *TurnResult
		queuedErr error
	)

	fixture.runtime.compactionOptions.Hook = func(
		ctx context.Context,
		event harness.HookEvent,
		_ compactionHookPayload,
	) error {
		if event != harness.HookEventPostCompact || queued != nil {
			return nil
		}

		queued, queuedErr = fixture.runtime.Run(
			ctx,
			TurnRequest{SessionID: &sessionID, Message: queuedSkillAgain},
		)

		return nil
	}

	runCompactionTurn(t, fixture, &sessionID, midTurnMessage)

	require.NoError(t, queuedErr)
	require.NotNil(t, queued)
	require.True(t, queued.Queued)

	rows := orderedMessages(t, fixture, sessionID)
	require.Equal(t, models.InjectedKindSkill, rows[1].InjectedKind)

	compaction, err := fixture.store.LatestCompaction(
		context.Background(),
		sessionID,
	)
	require.NoError(t, err)
	require.NotNil(t, compaction)
	require.GreaterOrEqual(
		t,
		compaction.ToSequence,
		rows[1].Sequence,
		"the skill message must be inside the compacted prefix",
	)

	requests := driver.Requests()
	last := requests[len(requests)-1].Messages
	assert.Equal(
		t,
		[]string{
			queuedSkillAgain,
			expectedSkillMessage(t, fixture, runtimeTestExplicitSkillName),
		},
		tailTexts(last, 2),
	)
}

// The ledger forgets the skills a compaction replaced, keeps the ones the
// remaining history holds, and never forgets what the turn itself delivered.
func TestSkillLedgerForgetsOnlyCompactedHistory(t *testing.T) {
	t.Parallel()

	skillRow := func(sequence int64, name string) *models.Message {
		return &models.Message{
			Role:         models.MessageRoleUser,
			Sequence:     sequence,
			InjectedKind: models.InjectedKindSkill,
			Content: fmt.Sprintf(
				skillTestMessageFormat,
				name,
				name+" skill",
				"/skills/"+name,
				"body",
			),
		}
	}

	ledger := newSkillLedgerFromHistory([]*models.Message{
		skillRow(1, "alpha"),
		skillRow(5, "bravo"),
	})
	ledger.record("charlie")

	assert.Equal(
		t,
		map[string]struct{}{
			"alpha":   {},
			"bravo":   {},
			"charlie": {},
		},
		ledger.held(),
	)

	ledger.forgetThrough(3)

	assert.Equal(
		t,
		map[string]struct{}{"bravo": {}, "charlie": {}},
		ledger.held(),
	)

	ledger.forgetThrough(5)

	assert.Equal(t, map[string]struct{}{"charlie": {}}, ledger.held())
}

func TestRuntimeRefusesADisabledCoreSkillNamedInAMessage(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first"),
		elelemtest.Text("must not run"),
	)
	fixture := newRuntimeFixtureWithOptions(
		t,
		driver,
		func(options *RuntimeOptions) {
			resolver, err := harness.NewResolver(
				options.ConfigDirectory,
				harness.Limits{},
				harness.WithDisabledCoreSkills([]string{"planning"}),
			)
			require.NoError(t, err)

			options.Resolver = resolver
		},
	)

	runSkillTurn(t, fixture, "carry on", nil)

	system := driver.Requests()[0].Messages[0].Text()
	assert.Contains(t, system, "core:freshness: ")
	assert.NotContains(t, system, "- core:planning: ")

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   skillTestCoreMessage,
		Workspace: fixture.workspace,
	})
	require.ErrorIs(t, err, harness.ErrSkillNotFound)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	assert.Nil(t, result)
	assert.Len(t, driver.Requests(), 1)

	var unknown *harness.UnknownSkillError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(
		t,
		`unknown skill "core:planning" in :core:planning; `+
			`available skills: core:freshness`,
		unknown.Message,
	)
}
