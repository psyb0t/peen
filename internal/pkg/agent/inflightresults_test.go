package agent

import (
	"context"
	"fmt"
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
