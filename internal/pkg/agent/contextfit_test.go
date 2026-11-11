package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fitTestSystem         = "You are a coding agent."
	fitTestAnswer         = "done"
	fitTestNoteWords      = "harbor compass timber stone engine canyon "
	fitTestNoteRepeats    = 400
	fitTestSmallResult    = "ok"
	fitTestReadArguments  = `{"path":"/app/notes/note%d.txt"}`
	fitTestListArguments  = `{"path":"/dir-%d"}`
	fitTestWriteArguments = `{"path":"/app/out/3.txt","content":"word\n"}`
	fitTestNoteRequest    = "Read note %d and write its first word."
	fitTestCrashRequest   = "Peen crashed. Finish every note."
	fitTestTurnEndNote    = "This turn did not finish."
	fitTestSummaryText    = "Notes 1 to 3 are done."
	fitTestQuestion       = "Question %d: list the directory."
	fitTestReply          = "Answer %d: the directory holds one file."
	fitTestLongQuestion   = "Question %d: "
	fitTestCurrent        = "Current request: read the plan."
	fitTestCallIDFormat   = "call-%s"
	// fitTestExchangeMessages is the most messages one test exchange adds.
	fitTestExchangeMessages = 4
)

func fitTestUser(text string) elelem.Message {
	return elelem.Message{Role: elelem.RoleUser, Content: elelem.Text(text)}
}

func fitTestSystemNote(text string) elelem.Message {
	return elelem.Message{Role: elelem.RoleSystem, Content: elelem.Text(text)}
}

func fitTestReplyMessage(text string) elelem.Message {
	return elelem.Message{
		Role: elelem.RoleAssistant, Content: elelem.Text(text),
	}
}

// fitTestRound is one assistant tool call with its result.
func fitTestRound(id, name, arguments, result string) []elelem.Message {
	callID := fmt.Sprintf(fitTestCallIDFormat, id)

	return []elelem.Message{
		{
			Role: elelem.RoleAssistant,
			ToolCalls: []elelem.ToolCall{{
				ID: callID, Name: name, Arguments: json.RawMessage(arguments),
			}},
		},
		{
			Role: elelem.RoleTool, ToolCallID: callID,
			Content: elelem.Text(result),
		},
	}
}

func fitTestNote(t *testing.T) string {
	t.Helper()

	wrapped, err := markToolOutput(t.Context(), toolNameReadFile,
		strings.Repeat(fitTestNoteWords, fitTestNoteRepeats), 0)
	require.NoError(t, err)

	return wrapped
}

// fitTestRun sends history with fitRequestContext as the limit handler and
// returns the messages the model received.
func fitTestRun(
	t *testing.T,
	history []elelem.Message,
	contextTokens, reserveTokens int,
) []elelem.Message {
	t.Helper()

	driver := elelemtest.NewScriptedDriver(elelemtest.Text(fitTestAnswer))
	request := elelem.NewRequest(elelem.New(driver)).
		WithModel(elelem.Model{ID: inFlightTestModel}).
		WithPrompt(elelem.NewPrompt().WithSystem(fitTestSystem).
			WithHistory(history)).
		WithMaxContextTokens(contextTokens).
		WithOutputReserveTokens(reserveTokens).
		PreMaxTokensReached(fitRequestContext).
		PostMaxTokensReached(fitInFlightToolResults)

	response, err := request.Run(t.Context())
	require.NoError(t, err)
	assert.Equal(t, fitTestAnswer, response.Text)

	last, found := driver.LastRequest()
	require.True(t, found)

	return last.Messages
}

func fitTestTokens(t *testing.T, messages []elelem.Message) int {
	t.Helper()

	count, err := elelem.DefaultTokenCounter().Count(messages, nil)
	require.NoError(t, err)

	return count
}

func fitTestTexts(messages []elelem.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, message := range messages {
		texts = append(texts, message.Text())
	}

	return texts
}

func fitTestNotice(messages []elelem.Message) (string, bool) {
	for _, message := range messages {
		if isDroppedNotice(message) {
			return message.Text(), true
		}
	}

	return "", false
}

func fitTestResult(messages []elelem.Message, id string) string {
	callID := fmt.Sprintf(fitTestCallIDFormat, id)
	for _, message := range messages {
		if message.Role == elelem.RoleTool && message.ToolCallID == callID {
			return message.Text()
		}
	}

	return ""
}

// A turn that a crash ended, followed by a turn that reads two large notes,
// must keep the summary, every request, and every turn-end note. The old
// note becomes a stub and the note the model is about to read stays whole.
func TestFitRequestContextMasksOldResultsAndKeepsTheRequests(
	t *testing.T,
) {
	t.Parallel()

	note := fitTestNote(t)
	history := slices.Concat(
		[]elelem.Message{
			fitTestUser(compactionSummaryLead + fitTestSummaryText),
		},
		fitTestRound("write", toolNameWriteFile, fitTestWriteArguments,
			fitTestSmallResult),
		[]elelem.Message{
			fitTestReplyMessage(fmt.Sprintf(fitTestReply, 3)),
			fitTestUser(fmt.Sprintf(fitTestNoteRequest, 4)),
			fitTestSystemNote(fitTestTurnEndNote),
			fitTestUser(fmt.Sprintf(fitTestNoteRequest, 5)),
			fitTestSystemNote(fitTestTurnEndNote),
			fitTestUser(fitTestCrashRequest),
		},
		fitTestRound("old", toolNameReadFile,
			fmt.Sprintf(fitTestReadArguments, 4), note),
		fitTestRound("live", toolNameReadFile,
			fmt.Sprintf(fitTestReadArguments, 5), note),
	)

	const (
		contextTokens = 5000
		reserveTokens = 500
	)

	require.Greater(t, fitTestTokens(t, history), contextTokens-reserveTokens,
		"the scenario must overflow the context")

	sent := fitTestRun(t, history, contextTokens, reserveTokens)

	assert.LessOrEqual(t, fitTestTokens(t, sent), contextTokens-reserveTokens)
	assert.Len(t, sent, len(history)+1, "nothing may be left out")

	texts := fitTestTexts(sent)
	for _, kept := range []string{
		compactionSummaryLead + fitTestSummaryText,
		fmt.Sprintf(fitTestNoteRequest, 4),
		fmt.Sprintf(fitTestNoteRequest, 5),
		fitTestCrashRequest,
	} {
		assert.Contains(t, texts, kept)
	}

	assert.Contains(t, fitTestResult(sent, "old"), maskedToolResultLead)
	assert.True(t, strings.HasPrefix(fitTestResult(sent, "old"),
		toolOutputOpenPrefix), "a stub keeps the tool output wrapper")
	assert.Equal(t, note, fitTestResult(sent, "live"))

	_, hasNotice := fitTestNotice(sent)
	assert.False(t, hasNotice)
}

// When stubs are not enough, the model's own earlier rounds leave before any
// message the user wrote, and a notice names the calls left out.
func TestFitRequestContextLeavesOutOldRoundsBeforeUserMessages(
	t *testing.T,
) {
	t.Parallel()

	const (
		exchanges     = 40
		contextTokens = 1300
		reserveTokens = 100
	)

	history := make([]elelem.Message, 0, fitTestExchangeMessages*exchanges)
	history = append(history,
		fitTestUser(compactionSummaryLead+fitTestSummaryText))

	for index := range exchanges {
		history = append(history,
			fitTestUser(fmt.Sprintf(fitTestQuestion, index)))
		history = append(history, fitTestRound(fmt.Sprint(index),
			toolNameListFiles, fmt.Sprintf(fitTestListArguments, index),
			fitTestSmallResult)...)
		history = append(history,
			fitTestReplyMessage(fmt.Sprintf(fitTestReply, index)))
	}

	history = append(history, fitTestUser(fitTestCurrent))
	history = append(history, fitTestRound("live", toolNameReadFile,
		fmt.Sprintf(fitTestReadArguments, 1), fitTestSmallResult)...)

	require.Greater(t, fitTestTokens(t, history), contextTokens-reserveTokens,
		"the scenario must overflow the context")

	sent := fitTestRun(t, history, contextTokens, reserveTokens)

	assert.LessOrEqual(t, fitTestTokens(t, sent), contextTokens-reserveTokens)

	notice, hasNotice := fitTestNotice(sent)
	require.True(t, hasNotice)
	assert.Contains(t, notice,
		toolNameListFiles+" "+fmt.Sprintf(fitTestListArguments, 0))

	texts := fitTestTexts(sent)
	assert.Contains(t, texts, compactionSummaryLead+fitTestSummaryText)
	assert.Contains(t, texts, fitTestCurrent)
	assert.Contains(t, texts, fmt.Sprintf(fitTestReply, exchanges-1),
		"replies leave only after every tool round")

	for index := range exchanges {
		assert.Contains(t, texts, fmt.Sprintf(fitTestQuestion, index))
	}

	assert.Equal(t, fitTestSmallResult, fitTestResult(sent, "live"))
	assert.Empty(t, fitTestResult(sent, "0"))
}

// Older user messages leave only when the model's rounds and replies are
// not enough, oldest first, and never the summary or the current request.
func TestFitRequestContextLeavesOutOlderMessagesLast(t *testing.T) {
	t.Parallel()

	const (
		exchanges       = 10
		questionRepeats = 20
		contextTokens   = 1200
		reserveTokens   = 100
	)

	question := func(index int) string {
		return fmt.Sprintf(fitTestLongQuestion, index) +
			strings.Repeat(fitTestNoteWords, questionRepeats)
	}

	history := make([]elelem.Message, 0, fitTestExchangeMessages*exchanges)
	history = append(history,
		fitTestUser(compactionSummaryLead+fitTestSummaryText))

	for index := range exchanges {
		history = append(history, fitTestUser(question(index)),
			fitTestReplyMessage(fmt.Sprintf(fitTestReply, index)))
	}

	history = append(history, fitTestUser(fitTestCurrent))
	history = append(history, fitTestRound("live", toolNameReadFile,
		fmt.Sprintf(fitTestReadArguments, 1), fitTestSmallResult)...)

	require.Greater(t, fitTestTokens(t, history), contextTokens-reserveTokens,
		"the scenario must overflow the context")

	sent := fitTestRun(t, history, contextTokens, reserveTokens)

	assert.LessOrEqual(t, fitTestTokens(t, sent), contextTokens-reserveTokens)

	_, hasNotice := fitTestNotice(sent)
	assert.True(t, hasNotice)

	texts := fitTestTexts(sent)
	assert.Contains(t, texts, compactionSummaryLead+fitTestSummaryText)
	assert.Contains(t, texts, fitTestCurrent)
	assert.NotContains(t, texts, question(0))
	assert.Contains(t, texts, question(exchanges-1))
	assert.Equal(t, fitTestSmallResult, fitTestResult(sent, "live"))
}

// A request that does not fit even without everything droppable is sent
// whole, because leaving history out would lose it for nothing.
func TestFitRequestContextKeepsHistoryThatCannotMakeRoom(t *testing.T) {
	t.Parallel()

	const (
		contextTokens = 600
		reserveTokens = 100
	)

	history := slices.Concat(
		[]elelem.Message{
			fitTestUser(compactionSummaryLead + fitTestSummaryText),
			fitTestUser(strings.Repeat(fitTestNoteWords, fitTestNoteRepeats)),
		},
		fitTestRound("live", toolNameReadFile,
			fmt.Sprintf(fitTestReadArguments, 1), fitTestSmallResult),
	)

	sent := fitTestRun(t, history, contextTokens, reserveTokens)

	assert.Len(t, sent, len(history)+1)

	_, hasNotice := fitTestNotice(sent)
	assert.False(t, hasNotice)
}

// A request under budget reaches the model unchanged.
func TestFitRequestContextLeavesARequestThatFitsAlone(t *testing.T) {
	t.Parallel()

	history := slices.Concat(
		[]elelem.Message{fitTestUser(fitTestCurrent)},
		fitTestRound("live", toolNameReadFile,
			fmt.Sprintf(fitTestReadArguments, 1), fitTestNote(t)),
	)

	sent := fitTestRun(t, history, 100000, 1000)

	require.Len(t, sent, len(history)+1)
	assert.Equal(t, fitTestTexts(history), fitTestTexts(sent[1:]))
}

func TestFitRequestContextNeverStubsWholeInFlightResults(t *testing.T) {
	t.Parallel()

	const (
		calls  = 40
		budget = 3000
	)

	history := inFlightTestHistory(t, calls)
	inFlightTestWithEvent(t, history, budget,
		func(ctx context.Context, event *elelem.TokenLimitEvent) error {
			before := slices.Clone(event.Messages)
			require.NoError(t, fitRequestContext(ctx, event))
			over, err := event.IsOverBudget()
			require.NoError(t, err)
			assert.True(t, over, "cutting alone cannot fit these results")
			assertInFlightResults(t, before, event.Messages, false, true)

			return nil
		})
}

func TestLeaveOutOldHistoryRecountsRestoredMessages(t *testing.T) {
	t.Parallel()

	history := slices.Concat([]elelem.Message{
		fitTestUser(fitTestCurrent),
	}, fitTestRound("old", toolNameListFiles,
		fitTestListArguments, fitTestSmallResult), []elelem.Message{
		fitTestUser(strings.Repeat(fitTestNoteWords, fitTestNoteRepeats)),
	})

	for _, withOlder := range []bool{false, true} {
		t.Run(fmt.Sprint(withOlder), func(t *testing.T) {
			t.Parallel()

			inFlightTestWithEvent(t, history, 100,
				func(ctx context.Context, event *elelem.TokenLimitEvent) error {
					before := slices.Clone(event.Messages)
					changed, err := leaveOutOldHistory(ctx, event, withOlder)
					require.NoError(t, err)
					assert.False(t, changed)
					assert.Equal(t, before, event.Messages)
					assert.Equal(t, fitTestTokens(t, before),
						event.EstimatedTokens)

					return nil
				})
		})
	}
}

func TestPartialHistoryKeepsMessagesWhenNoticeCostsMore(t *testing.T) {
	t.Parallel()

	history := []elelem.Message{
		fitTestUser(fitTestCurrent), fitTestReplyMessage(fitTestSmallResult),
		fitTestUser(strings.Repeat(inFlightTestWords, 1000)),
	}
	inFlightTestWithEvent(t, history, 100,
		func(ctx context.Context, event *elelem.TokenLimitEvent) error {
			before := slices.Clone(event.Messages)
			changed, err := leaveOutPartialHistory(ctx, event)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, before, event.Messages)
			assert.Equal(t, fitTestTokens(t, before), event.EstimatedTokens)

			return nil
		})
}

func TestFitRequestContextLeavesOlderMessagesBeforeWholeResults(
	t *testing.T,
) {
	t.Parallel()

	const (
		calls  = 40
		budget = 6000
	)

	older := strings.Repeat(fitTestNoteWords, fitTestNoteRepeats)
	history := slices.Concat([]elelem.Message{
		fitTestUser(older), fitTestReplyMessage(fitTestAnswer),
	}, inFlightTestHistory(t, calls))

	inFlightTestWithEvent(t, history, budget,
		func(ctx context.Context, event *elelem.TokenLimitEvent) error {
			require.NoError(t, fitRequestContext(ctx, event))
			over, err := event.IsOverBudget()
			require.NoError(t, err)
			assert.False(t, over)
			assert.NotContains(t, fitTestTexts(event.Messages), older)
			_, hasNotice := fitTestNotice(event.Messages)
			assert.True(t, hasNotice)

			for _, message := range event.Messages {
				if message.Role == elelem.RoleTool {
					assert.NotEqual(t, inFlightResultLeftOut, message.Text())
				}
			}

			return nil
		})
}

// A compaction activity stubs old tool results in the saved context before
// it considers a summary, and leaves a context that fits as it was.
func TestMaskWorkflowToolResultsStubsOldResultsInTheSavedContext(
	t *testing.T,
) {
	t.Parallel()

	note := fitTestNote(t)

	messages := slices.Concat(
		[]elelem.Message{fitTestUser(fitTestCrashRequest)},
		fitTestRound("old", toolNameReadFile,
			fmt.Sprintf(fitTestReadArguments, 4), note),
		fitTestRound("live", toolNameReadFile,
			fmt.Sprintf(fitTestReadArguments, 5), note),
	)

	resolver := childConversationModelResolver{model: ModelClient{
		Client: elelem.New(elelemtest.NewScriptedDriver()),
		Model:  elelem.Model{ID: inFlightTestModel},
	}}

	input := func(contextTokens int) workflowRoundInput {
		encoded, err := json.Marshal(workflowConversation{
			ModelReference: inFlightTestModel, SystemPrompt: fitTestSystem,
			Messages: messages, MaxContextTokens: contextTokens,
			OutputReserveTokens: 500,
		})
		require.NoError(t, err)

		return workflowRoundInput{
			Execution: workflowExecutionInput{Context: encoded},
		}
	}

	roomy := input(100000)
	unchanged, err := maskWorkflowToolResults(t.Context(), resolver, roomy)
	require.NoError(t, err)
	assert.JSONEq(t, string(roomy.Execution.Context),
		string(unchanged.Execution.Context))

	masked, err := maskWorkflowToolResults(t.Context(), resolver, input(5000))
	require.NoError(t, err)

	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(masked.Execution.Context,
		&conversation))
	require.Len(t, conversation.Messages, len(messages))
	assert.Contains(t, fitTestResult(conversation.Messages, "old"),
		maskedToolResultLead)
	assert.Equal(t, note, fitTestResult(conversation.Messages, "live"))
	assert.Equal(t, fitTestCrashRequest, conversation.Messages[0].Text())
}

func TestClipCompactionToolResultKeepsTheStartAndEnd(t *testing.T) {
	t.Parallel()

	long := "start " + strings.Repeat("é", 3000) + " end"

	testCases := []struct {
		name  string
		text  string
		limit int
		check func(t *testing.T, clipped string)
	}{
		{
			name: "short result stays", text: "short", limit: 4000,
			check: func(t *testing.T, clipped string) {
				t.Helper()
				assert.Equal(t, "short", clipped)
			},
		},
		{
			name: "long result keeps both ends", text: long, limit: 1000,
			check: func(t *testing.T, clipped string) {
				t.Helper()
				assert.True(t, strings.HasPrefix(clipped, "start "))
				assert.True(t, strings.HasSuffix(clipped, " end"))
				assert.Contains(t, clipped, "bytes left out")
				assert.Less(t, len(clipped), len(long))
				assert.True(t, utf8.ValidString(clipped))
			},
		},
		{
			name: "zero limit leaves it out", text: long, limit: 0,
			check: func(t *testing.T, clipped string) {
				t.Helper()
				assert.Equal(t, compactionToolResultOmitted, clipped)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.check(t, clipCompactionToolResult(tc.text, tc.limit))
		})
	}
}

// The summarizer is asked again with shorter tool results only while it
// refuses the transcript as too large and a shorter one exists.
func TestSummarizeShrinkingTranscriptRetriesWithShorterResults(
	t *testing.T,
) {
	t.Parallel()

	render := func(limit int) string {
		return strings.Repeat("x", limit+1)
	}

	refuseOver := func(size int, calls *[]int) func(string) (
		compactionSummary, error,
	) {
		return func(transcript string) (compactionSummary, error) {
			*calls = append(*calls, len(transcript))
			if len(transcript) > size {
				return compactionSummary{}, ctxerrors.Wrap(
					ErrCompactionUnavailable, "too large")
			}

			return compactionSummary{Text: fitTestSummaryText}, nil
		}
	}

	t.Run("shrinks until it fits", func(t *testing.T) {
		t.Parallel()

		var calls, shrunk []int

		summary, err := summarizeShrinkingTranscript(render,
			refuseOver(compactionToolResultTightBytes+1, &calls),
			func(_ error, limit int) { shrunk = append(shrunk, limit) })
		require.NoError(t, err)
		assert.Equal(t, fitTestSummaryText, summary.Text)
		assert.Equal(t, []int{
			compactionToolResultBytes + 1,
			compactionToolResultTightBytes + 1,
		}, calls)
		assert.Equal(t, []int{compactionToolResultTightBytes}, shrunk)
	})

	t.Run("gives up after the last limit", func(t *testing.T) {
		t.Parallel()

		var calls []int

		_, err := summarizeShrinkingTranscript(render,
			refuseOver(0, &calls), func(error, int) {})
		require.ErrorIs(t, err, ErrCompactionUnavailable)
		assert.Len(t, calls, len(compactionToolResultLimits()))
	})

	t.Run("other failures are not retried", func(t *testing.T) {
		t.Parallel()

		calls := 0

		_, err := summarizeShrinkingTranscript(render,
			func(string) (compactionSummary, error) {
				calls++

				return compactionSummary{}, ErrCompactionEmptySummary
			}, func(error, int) {})
		require.ErrorIs(t, err, ErrCompactionEmptySummary)
		assert.Equal(t, 1, calls)
	})

	t.Run("a transcript that cannot shrink is not sent again",
		func(t *testing.T) {
			t.Parallel()

			var calls []int

			_, err := summarizeShrinkingTranscript(
				func(int) string { return fitTestSummaryText },
				refuseOver(0, &calls), func(error, int) {})
			require.ErrorIs(t, err, ErrCompactionUnavailable)
			assert.Len(t, calls, 1)
		})
}
