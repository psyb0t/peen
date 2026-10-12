package agent

import (
	"context"
	"testing"
	"time"

	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectoryContinuationRetainsDeadlineUsageAndReasoning(t *testing.T) {
	t.Parallel()
	first := elelemtest.ToolCall("first", toolNameChangeDirectory, `{}`)
	first.Deltas[0].Reasoning = "first"
	first.Usage = elelem.Usage{TokenCounts: elelem.TokenCounts{Prompt: 10, Completion: 2, Total: 12, CacheRead: 1}}
	second := elelemtest.ToolCall("second", toolNameChangeDirectory, `{}`)
	second.Deltas[0].Reasoning = "second"
	second.Usage = elelem.Usage{TokenCounts: elelem.TokenCounts{Prompt: 20, Completion: 3, Total: 23, CacheWrite: 1, CacheWriteLongTTL: 1}}
	last := elelemtest.Thinking("last", "done").WithUsage(elelem.Usage{TokenCounts: elelem.TokenCounts{Prompt: 30, Completion: 4, Total: 34}})
	driver := elelemtest.NewScriptedDriver(first, second, last)
	scope := &directoryScope{calls: map[string]directoryCall{}, results: map[string]string{}}
	var tool elelem.Tool
	tool = elelem.Tool{
		Name:            toolNameChangeDirectory,
		Description:     "Change the test scope.",
		ArgumentsSchema: []byte(`{"type":"object","properties":{}}`),
		Handler: func(_ context.Context, input elelem.ToolInput) (elelem.ToolResult, error) {
			scope.mutex.Lock()
			scope.current = &directoryContext{prompt: input.CallID, tools: elelem.NewToolSet(tool)}
			scope.mutex.Unlock()

			return elelem.ToolResult{Content: "changed"}, nil
		},
	}
	scope.current = &directoryContext{prompt: "initial", tools: elelem.NewToolSet(tool)}
	scope.applied = scope.current
	var deadlines []time.Time
	request := elelem.NewRequest(elelem.New(driver)).WithModel(elelem.Model{ID: runtimeTestModelID}).
		WithPrompt(elelem.NewPrompt().WithSystem("initial").UserText("hop twice")).
		WithToolProvider(scope.provideTools).WithAutoToolCalls().
		WithMaxRounds(4).OnRoundStart(scope.beforeRound).OnRoundEnd(scope.afterRound).
		OnDelta(func(ctx context.Context, _ elelem.Delta) error {
			deadline, found := ctx.Deadline()
			require.True(t, found)
			deadlines = append(deadlines, deadline)

			return nil
		})
	response, err := scope.run(context.Background(), request, 4, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "done", response.Text)
	assert.Equal(t, "firstsecondlast", response.Reasoning)
	assert.Equal(t, int64(60), response.Usage.Prompt)
	assert.Equal(t, int64(9), response.Usage.Completion)
	assert.Equal(t, int64(69), response.Usage.Total)
	assert.Equal(t, int64(1), response.Usage.CacheRead)
	assert.Equal(t, int64(1), response.Usage.CacheWrite)
	assert.Equal(t, int64(1), response.Usage.CacheWriteLongTTL)
	require.NotEmpty(t, deadlines)
	for _, deadline := range deadlines {
		assert.Equal(t, deadlines[0], deadline)
	}
	require.Len(t, driver.Requests(), 3)
}
