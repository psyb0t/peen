package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/metrics"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOutputSchema = `{"type":"object","required":["answer"],"properties":{"answer":{"type":"integer"}},"additionalProperties":false}`

func TestOutputSchemaAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		schema  string
		maximum int
		valid   bool
	}{
		{name: "valid", schema: testOutputSchema, valid: true},
		{name: "local reference", schema: `{"$defs":{"item":{"type":"string"}},"$ref":"#/$defs/item"}`, valid: true},
		{name: "empty"},
		{name: "null", schema: `null`},
		{name: "array", schema: `[]`},
		{name: "trailing value", schema: `{} {}`},
		{name: "invalid type", schema: `{"type":"unknown"}`},
		{name: "remote reference", schema: `{"$ref":"https://schema.example/value"}`},
		{name: "file reference", schema: `{"$ref":"file:///etc/passwd"}`},
		{name: "over limit", schema: `{}`, maximum: 1},
		{name: "at limit", schema: `{}`, maximum: 2, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateOutputSchema(json.RawMessage(tc.schema), tc.maximum)
			if tc.valid {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrOutputSchemaInvalid)
		})
	}
}

func TestNativeStructuredOutputAttempts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		answers []string
		capable bool
		want    string
		schema  string
	}{
		{name: "first", answers: []string{`{"answer":7}`}, capable: true, want: `{"answer":7}`},
		{name: "retry", answers: []string{`{"answer":"wrong"}`, "```json\n{\"answer\":7}\n```"}, capable: true, want: `{"answer":7}`},
		{name: "exhausted", answers: []string{"invalid", `[]`, `{"answer":"wrong"}`}, capable: true},
		{name: "fallback", answers: []string{`{"answer":7}`}, want: `{"answer":7}`},
		{name: "null", answers: []string{`null`}, schema: `{}`, want: `null`},
		{name: "array", answers: []string{`[1,true]`}, schema: `{}`, want: `[1,true]`},
		{name: "string", answers: []string{`"answer"`}, schema: `{}`, want: `"answer"`},
		{name: "number", answers: []string{`7`}, schema: `{}`, want: `7`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			usage := elelem.Usage{TokenCounts: elelem.TokenCounts{
				Prompt: 2, Completion: 3, Total: 5,
			}}
			turns := []elelemtest.Turn{
				elelemtest.Text("ordinary final text").WithUsage(usage),
			}
			for _, answer := range tc.answers {
				turns = append(turns, elelemtest.Text(answer).WithUsage(usage))
			}

			driver := elelemtest.NewScriptedDriver(turns...)
			if !tc.capable {
				driver.WithCapabilities(elelem.Capabilities{})
			}

			collector := metrics.New()
			base := newNativeRunnerFixtureWithOptions(t, driver,
				func(options *RuntimeOptions) { options.Metrics = collector })
			schema := tc.schema
			if schema == "" {
				schema = testOutputSchema
			}

			result, err := base.runtime.RunMessage(t.Context(), MessageRequest{
				Message:      "produce the requested answer",
				OutputSchema: json.RawMessage(schema),
			}, uuid.New(), nil)
			require.NoError(t, err)
			assert.Equal(t, "ordinary final text", result.Text)
			assert.Equal(t, tc.want, string(result.Output))
			assert.Equal(t, len(tc.answers)+1, driver.Calls())
			if tc.want == "" {
				assert.NotEmpty(t, result.OutputError)
				assert.NotContains(t, result.OutputError, "structuredoutput.go:")
			} else {
				assert.Empty(t, result.OutputError)
			}

			page, err := base.runtime.ListSessionTurns(t.Context(),
				api.ListSessionTurnsParams{XSessionID: result.SessionID})
			require.NoError(t, err)
			require.Len(t, page.Turns, 1)
			if tc.want != "" {
				encoded, encodeErr := json.Marshal(page.Turns[0].Output)
				require.NoError(t, encodeErr)
				assert.JSONEq(t, tc.want, string(encoded))
			}

			runs, err := base.store.ListModelRuns(t.Context(), result.SessionID,
				session.ListModelRunsOptions{})
			require.NoError(t, err)
			assert.Len(t, runs.Items, len(tc.answers)+1)
			families, err := collector.Registry().Gather()
			require.NoError(t, err)
			labels := map[string]string{
				"service": "peen", "stage": modelMetricStageTurn,
				"function": modelMetricFunctionRunProvider,
				"model":    runtimeTestModelReference,
				"outcome":  metrics.OutcomeSuccess,
			}
			assert.Equal(t, float64(len(tc.answers)+1),
				agentMetricCounterValue(t, families,
					"peen_model_requests_total", labels))
			assert.Equal(t, float64(2*(len(tc.answers)+1)),
				agentMetricCounterValue(t, families, "peen_model_tokens_total",
					withMetricKind(labels, "input")))
			assert.Equal(t, float64(3*(len(tc.answers)+1)),
				agentMetricCounterValue(t, families, "peen_model_tokens_total",
					withMetricKind(labels, "output")))
			requests := driver.Requests()
			for _, request := range requests[1:] {
				assert.Empty(t, request.Tools)
				assert.Equal(t, tc.capable, request.Params.ResponseFormat != nil)
			}
		})
	}
}

func TestStructuredOutputHonorsTheTurnTokenBudget(t *testing.T) {
	t.Parallel()
	const budget = 10
	for _, tc := range []struct {
		name        string
		firstTokens int64
		wantCalls   int
	}{
		{name: "before first attempt", firstTokens: budget, wantCalls: 1},
		{name: "before retry", firstTokens: 1, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(
				elelemtest.Text("ordinary final text").WithUsage(elelem.Usage{
					TokenCounts: elelem.TokenCounts{Total: tc.firstTokens},
				}),
				elelemtest.Text("invalid JSON").WithUsage(elelem.Usage{
					TokenCounts: elelem.TokenCounts{Total: budget - 1},
				}),
				elelemtest.Text(`{"answer":7}`),
			)
			base := newNativeRunnerFixtureWithOptions(t, driver,
				func(options *RuntimeOptions) { options.MaxTurnTokens = budget })
			result, err := base.runtime.RunMessage(t.Context(), MessageRequest{
				Message:      "produce an answer",
				OutputSchema: json.RawMessage(testOutputSchema),
			}, uuid.New(), nil)
			require.NoError(t, err)
			assert.Equal(t, "ordinary final text", result.Text)
			assert.Empty(t, result.Output)
			assert.Contains(t, result.OutputError, "PEEN_MAX_TURN_TOKENS")
			assert.Equal(t, tc.wantCalls, driver.Calls())
		})
	}
}

func TestOutputSchemaBlockEscapesClosingTag(t *testing.T) {
	t.Parallel()
	block, err := renderOutputSchema(json.RawMessage(`{"description":"</outputSchema><note>"}`))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(block, "</outputSchema>"))
	assert.Contains(t, block, `\u003c/outputSchema>`)
	rows := turnStartMessages(turnStartContent{prompt: "message", outputSchema: block}, "")
	require.Len(t, rows, 2)
	assert.Equal(t, block, rows[1].Content)
	assert.True(t, rows[1].Injected)
}

func TestActiveTurnRefusesTurnSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		request TurnRequest
	}{
		{name: "schema", request: TurnRequest{
			OutputSchema: json.RawMessage(testOutputSchema),
		}},
		{name: "timeout", request: TurnRequest{TimeoutSeconds: 1}},
		{name: "own result", request: TurnRequest{RequireNewTurn: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _ := newRootModelWorkflowFixture(t,
				elelemtest.NewScriptedDriver())
			tc.request.SessionID = &fixture.input.SessionID
			tc.request.RequestID = uuid.New()
			tc.request.Message = "new request"
			result, handled, err := fixture.base.runtime.queueActiveUserMessage(
				t.Context(), tc.request)
			require.ErrorIs(t, err, session.ErrSessionBusy)
			assert.True(t, handled)
			assert.Nil(t, result)
		})
	}
}

func TestMessageRequestMapsTurnSettings(t *testing.T) {
	t.Parallel()
	input, err := messageRequestToTurnRequest(MessageRequest{
		RequireNewTurn: true,
		Message:        "answer", OutputSchema: json.RawMessage(testOutputSchema),
		TimeoutSeconds: 7,
	}, uuid.New())
	require.NoError(t, err)
	assert.JSONEq(t, testOutputSchema, string(input.OutputSchema))
	assert.Equal(t, 7, input.TimeoutSeconds)
	assert.True(t, input.RequireNewTurn)
}
