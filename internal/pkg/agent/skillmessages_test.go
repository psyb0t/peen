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

func TestRuntimeNotesASkillAlreadyLoadedEarlierInTheConversation(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first reply"),
		elelemtest.Text("second reply"),
	)
	fixture := newRuntimeFixture(t, driver)
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
