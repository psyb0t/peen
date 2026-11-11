package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	inFlightTestContextTokens = 4000
	inFlightTestReserveTokens = 500
	inFlightTestCalls         = 3
	inFlightTestWordRepeats   = 800
	inFlightTestWords         = "alpha beta gamma delta "
	inFlightTestCallIDFormat  = "call-%d"
	inFlightTestAnswer        = "done"
	inFlightTestQuestion      = "Read the three files."
	inFlightTestArguments     = "{}"
	inFlightTestModel         = "mock-model"
)

// parallelToolCallTurn is one model reply that calls toolName count times in
// a single round.
func parallelToolCallTurn(toolName string, count int) elelemtest.Turn {
	deltas := make([]elelem.Delta, 0, count)

	for index := range count {
		deltas = append(deltas, elelem.Delta{ToolCall: &elelem.ToolCallDelta{
			Index:     index,
			ID:        fmt.Sprintf(inFlightTestCallIDFormat, index),
			Name:      toolName,
			Arguments: inFlightTestArguments,
		}})
	}

	deltas[len(deltas)-1].FinishReason = elelem.FinishReasonToolCalls

	return elelemtest.Turn{Deltas: deltas}
}

// runParallelLargeResults sends one round of parallel calls whose wrapped
// results together hold more than the context, and returns the request the
// model received next.
func runParallelLargeResults(
	t *testing.T,
	fitResults bool,
) elelem.DriverRequest {
	t.Helper()

	driver := elelemtest.NewScriptedDriver(
		parallelToolCallTurn(toolNameReadFile, inFlightTestCalls),
		elelemtest.Text(inFlightTestAnswer),
	)

	wrapped, err := markToolOutput(t.Context(), toolNameReadFile,
		strings.Repeat(inFlightTestWords, inFlightTestWordRepeats), 0)
	require.NoError(t, err)

	tools := elelem.NewToolSet(elelem.Tool{
		Name: toolNameReadFile,
		Handler: func(context.Context, elelem.ToolInput) (
			elelem.ToolResult, error,
		) {
			return elelem.ToolResult{Content: wrapped}, nil
		},
	})

	request := elelem.NewRequest(elelem.New(driver)).
		WithModel(elelem.Model{ID: inFlightTestModel}).
		WithPrompt(elelem.NewPrompt().UserText(inFlightTestQuestion)).
		WithTools(tools).WithAutoToolCalls().
		WithMaxContextTokens(inFlightTestContextTokens).
		WithOutputReserveTokens(inFlightTestReserveTokens)
	if fitResults {
		request = request.PostMaxTokensReached(fitInFlightToolResults)
	}

	response, err := request.Run(t.Context())
	require.NoError(t, err)
	assert.Equal(t, inFlightTestAnswer, response.Text)

	last, found := driver.LastRequest()
	require.True(t, found)

	return last
}

func requestTokens(t *testing.T, request elelem.DriverRequest) int {
	t.Helper()

	count, err := elelem.DefaultTokenCounter().Count(request.Messages,
		request.Tools)
	require.NoError(t, err)

	return count
}

func TestParallelToolResultsAreCutToFitTheContext(t *testing.T) {
	t.Parallel()

	unfitted := runParallelLargeResults(t, false)
	require.Greater(t, requestTokens(t, unfitted), inFlightTestContextTokens,
		"the scenario must overflow the context without the hook")

	fitted := runParallelLargeResults(t, true)
	assert.LessOrEqual(t, requestTokens(t, fitted),
		inFlightTestContextTokens-inFlightTestReserveTokens)

	notice := fmt.Sprintf(toolOutputNoticeFormat,
		toolOutputSource(toolNameReadFile), toolNameReadFile)
	results := 0

	for _, message := range fitted.Messages {
		if message.Role != elelem.RoleTool {
			continue
		}

		results++
		text := message.Text()
		assert.True(t, strings.HasPrefix(text, toolOutputOpenPrefix), text)
		assert.Contains(t, text, workflowToolTruncatedMarker)
		assert.True(t, strings.HasSuffix(text, notice), text)
	}

	assert.Equal(t, inFlightTestCalls, results)
}

func TestFairSharesGiveSmallResultsAllTheyNeed(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		sizes []int
		room  int
		want  []int
	}{
		{
			name:  "everything fits",
			sizes: []int{200, 300},
			room:  1000,
			want:  []int{200, 300},
		},
		{
			name:  "small result keeps all of it",
			sizes: []int{3000, 150, 3000},
			room:  2150,
			want:  []int{1000, 150, 1000},
		},
		{
			name:  "no room still keeps the floor",
			sizes: []int{3000, 3000},
			room:  0,
			want:  []int{inFlightResultFloorTokens, inFlightResultFloorTokens},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, fairShares(tc.sizes, tc.room))
		})
	}
}

func inFlightTestHistory(t *testing.T, calls int) []elelem.Message {
	t.Helper()

	turn := parallelToolCallTurn(toolNameReadFile, calls)
	callMessage := elelem.Message{Role: elelem.RoleAssistant}
	results := make([]elelem.Message, 0, calls)

	for _, delta := range turn.Deltas {
		call := delta.ToolCall
		callMessage.ToolCalls = append(callMessage.ToolCalls, elelem.ToolCall{
			ID: call.ID, Name: call.Name,
			Arguments: []byte(inFlightTestArguments),
		})
		results = append(results, elelem.Message{
			Role: elelem.RoleTool, ToolCallID: call.ID,
			Content: elelem.Text(fitTestNote(t)),
		})
	}

	return slices.Concat([]elelem.Message{
		fitTestUser(inFlightTestQuestion), callMessage,
	}, results)
}

// inFlightTestWithEvent uses the engine's initialized token counter, which
// is private to Elelem, and runs check even when the request already fits.
func inFlightTestWithEvent(
	t *testing.T,
	history []elelem.Message,
	budget int,
	check elelem.TokenLimitHandler,
) {
	t.Helper()

	driver := elelemtest.NewScriptedDriver(elelemtest.Text(inFlightTestAnswer))
	request := elelem.NewRequest(elelem.New(driver)).
		WithModel(elelem.Model{ID: inFlightTestModel}).
		WithPrompt(elelem.NewPrompt().WithHistory(history)).
		WithMaxContextTokens(1).
		WithOutputReserveTokens(0).
		PreMaxTokensReached(func(ctx context.Context,
			event *elelem.TokenLimitEvent,
		) error {
			event.BudgetTokens = budget

			return check(ctx, event)
		})

	_, err := request.Run(t.Context())
	require.NoError(t, err)
}

func TestInFlightResultsFitWithoutLosingCalls(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		calls     int
		budget    int
		wantStubs bool
		wantCuts  bool
	}{
		{
			name: "too many floors", calls: 40, budget: 3000,
			wantStubs: true, wantCuts: true,
		},
		{
			name: "cutting is enough", calls: 3, budget: 3500,
			wantCuts: true,
		},
		{name: "already fits", calls: 3, budget: 100000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			history := inFlightTestHistory(t, tc.calls)
			inFlightTestWithEvent(t, history, tc.budget,
				func(ctx context.Context, event *elelem.TokenLimitEvent) error {
					before := slices.Clone(event.Messages)
					require.NoError(t, fitInFlightToolResults(ctx, event))
					over, err := event.IsOverBudget()
					require.NoError(t, err)
					assert.False(t, over)
					assertInFlightResults(t, before, event.Messages,
						tc.wantStubs, tc.wantCuts)

					return nil
				})
		})
	}
}

func assertInFlightResults(
	t *testing.T,
	before, after []elelem.Message,
	wantStubs, wantCuts bool,
) {
	t.Helper()

	require.Len(t, after, len(before))
	stubbed := false
	cut := false

	for index, message := range after {
		assert.Equal(t, before[index].ToolCallID, message.ToolCallID)
		assert.Equal(t, before[index].ToolCalls, message.ToolCalls)
		if message.Role != elelem.RoleTool {
			continue
		}

		if message.Text() == inFlightResultLeftOut {
			stubbed = true

			continue
		}

		assert.False(t, stubbed, "only the newest results may be stubbed")
		cut = cut || message.Text() != before[index].Text()
	}

	assert.Equal(t, wantStubs, stubbed)
	assert.Equal(t, wantCuts, cut)
	assert.NotEqual(t, inFlightResultLeftOut, after[2].Text())
	assert.Contains(t, after[2].Text(), strings.Fields(fitTestNoteWords)[0])
	if !wantCuts {
		assert.Equal(t, before, after)
	}
}

func TestPostHookKeepsPartialHistoryBeforeStubbing(t *testing.T) {
	t.Parallel()

	const budget = 4000

	older := strings.Repeat(inFlightTestWords, 100)
	history := slices.Concat([]elelem.Message{
		fitTestUser(older), fitTestReplyMessage(fitTestAnswer),
	}, inFlightTestHistory(t, 40))
	stubCounts := make([]int, 0, 2)

	for _, withHistory := range []bool{false, true} {
		inFlightTestWithEvent(t, history, budget,
			func(ctx context.Context, event *elelem.TokenLimitEvent) error {
				original := slices.Clone(event.Messages)
				require.NoError(t, cutInFlightToolResults(ctx, event))
				if withHistory {
					changed, err := leaveOutPartialHistory(ctx, event)
					require.NoError(t, err)
					assert.True(t, changed)
					over, err := event.IsOverBudget()
					require.NoError(t, err)
					assert.True(t, over, "history alone cannot fit")
					assert.NotContains(t, fitTestTexts(event.Messages), older)
					assert.Zero(t, inFlightStubCount(event.Messages))
					event.Messages = original
					require.NoError(t, fitInFlightToolResults(ctx, event))
					assert.NotContains(t, fitTestTexts(event.Messages), older)
				} else {
					require.NoError(t, leaveOutInFlightResults(ctx, event,
						trailingToolResults(event.Messages)))
				}

				over, err := event.IsOverBudget()
				require.NoError(t, err)
				assert.False(t, over)
				assertFitToolPairs(t, event.Messages)
				stubCounts = append(stubCounts, inFlightStubCount(event.Messages))

				return nil
			})
	}

	assert.Less(t, stubCounts[1], stubCounts[0])
}

func inFlightStubCount(messages []elelem.Message) int {
	count := 0
	for _, message := range messages {
		if message.Role == elelem.RoleTool &&
			message.Text() == inFlightResultLeftOut {
			count++
		}
	}

	return count
}

func assertFitToolPairs(t *testing.T, messages []elelem.Message) {
	t.Helper()

	calls := make(map[string]int)
	results := make(map[string]int)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			calls[call.ID]++
		}

		if message.Role == elelem.RoleTool {
			results[message.ToolCallID]++
		}
	}

	assert.Equal(t, calls, results)
	for _, count := range calls {
		assert.Equal(t, 1, count)
	}
}

func TestPostHookKeepsPartialHistoryWithoutTrailingResults(t *testing.T) {
	t.Parallel()

	older := strings.Repeat(inFlightTestWords, 100)
	history := []elelem.Message{
		fitTestUser(older), fitTestReplyMessage(fitTestAnswer),
		fitTestUser(strings.Repeat(inFlightTestWords, 1000)),
	}

	inFlightTestWithEvent(t, history, 100,
		func(ctx context.Context, event *elelem.TokenLimitEvent) error {
			beforeMessages := slices.Clone(event.Messages)
			require.Empty(t, trailingToolResults(event.Messages))
			require.NoError(t, fitRequestContext(ctx, event))
			assert.Equal(t, beforeMessages, event.Messages, "ladder restores")
			before := event.EstimatedTokens
			require.NoError(t, fitInFlightToolResults(ctx, event))
			assert.Less(t, event.EstimatedTokens, before)
			assert.Equal(t, fitTestTokens(t, event.Messages),
				event.EstimatedTokens)
			assert.NotContains(t, fitTestTexts(event.Messages), older)
			notice, found := fitTestNotice(event.Messages)
			require.True(t, found)
			assert.Equal(t, droppedHistoryNotice(1, 1, nil), notice)
			assertFitToolPairs(t, event.Messages)

			return nil
		})
}

func TestReasoningLeavesOldestFirst(t *testing.T) {
	t.Parallel()

	const reasoning = "alpha beta gamma delta "

	first := fitTestReplyMessage(fitTestAnswer)
	first.Reasoning = strings.Repeat(reasoning, 500)
	first.ProviderReasoning = []byte(`{"reasoning":"alpha"}`)
	second := first
	history := []elelem.Message{fitTestUser(fitTestCurrent), first, second}
	withoutFirst := slices.Clone(history)
	withoutFirst[1].Reasoning = ""
	withoutFirst[1].ProviderReasoning = nil

	testCases := []struct {
		name    string
		budget  int
		cleared []int
	}{
		{name: "fits", budget: fitTestTokens(t, history)},
		{
			name: "oldest enough", budget: fitTestTokens(t, withoutFirst),
			cleared: []int{1},
		},
		{name: "newest reasoning only", budget: 100, cleared: []int{1, 2}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inFlightTestWithEvent(t, history, tc.budget,
				func(ctx context.Context, event *elelem.TokenLimitEvent) error {
					require.NoError(t, leaveOutReasoning(ctx, event))
					for index, message := range event.Messages {
						if slices.Contains(tc.cleared, index) {
							assert.Empty(t, message.Reasoning)
							assert.Nil(t, message.ProviderReasoning)

							continue
						}

						expected := history[index]
						expected.Origin = elelem.MessageOriginSeed
						assert.Equal(t, expected, message)
					}

					over, err := event.IsOverBudget()
					require.NoError(t, err)
					assert.False(t, over)
					assertFitToolPairs(t, event.Messages)

					return nil
				})
		})
	}

	assert.Equal(t, first, history[1], "caller history keeps reasoning")
}

func TestPostHookPreservesLiveToolReasoning(t *testing.T) {
	t.Parallel()

	history := inFlightTestHistory(t, 1)
	history[1].Reasoning = strings.Repeat(inFlightTestWords, 1000)
	history[1].ProviderReasoning = []byte(`{"reasoning":"alpha"}`)

	inFlightTestWithEvent(t, history, 100,
		func(ctx context.Context, event *elelem.TokenLimitEvent) error {
			before := event.Messages[1]
			require.NoError(t, fitInFlightToolResults(ctx, event))
			assert.Equal(t, before, event.Messages[1])
			assert.Equal(t, inFlightResultLeftOut, event.Messages[2].Text())
			assertFitToolPairs(t, event.Messages)

			return nil
		})
}

func TestPostHookClearsNewestReasoningWithoutToolCalls(t *testing.T) {
	t.Parallel()

	message := elelem.Message{
		Role:              elelem.RoleAssistant,
		Reasoning:         strings.Repeat(inFlightTestWords, 1000),
		ProviderReasoning: []byte(`{"reasoning":"alpha"}`),
	}
	history := slices.Concat([]elelem.Message{fitTestUser(fitTestCurrent)},
		fitTestRound("live", toolNameReadFile, inFlightTestArguments,
			fitTestSmallResult), []elelem.Message{message})
	inFlightTestWithEvent(t, history, 100,
		func(ctx context.Context, event *elelem.TokenLimitEvent) error {
			require.NoError(t, fitInFlightToolResults(ctx, event))
			require.Len(t, event.Messages, len(history))
			last := event.Messages[len(event.Messages)-1]
			assert.Equal(t, elelem.RoleAssistant, last.Role)
			assert.Empty(t, last.Reasoning)
			assert.Nil(t, last.ProviderReasoning)
			over, err := event.IsOverBudget()
			require.NoError(t, err)
			assert.False(t, over)
			assertFitToolPairs(t, event.Messages)

			return nil
		})
	assert.Equal(t, message, history[len(history)-1])
}
