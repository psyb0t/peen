package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	modelAuditTestReference = "gateway/audit-model"
	modelAuditTestModelID   = "audit-model"
)

func TestRuntimeModelAuditPersistsRootAndChildProviderRounds(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(
			launchAgentCallID,
			toolNameLaunchAgent,
			launchAgentArguments(t, launchAgentInput{
				Task:  "inspect this workspace",
				Agent: launchAgentChildName,
			}),
		),
		elelemtest.Thinking("child reasoning", "child result"),
		elelemtest.Thinking("root reasoning", "root result"),
	)
	fixture := newLaunchAgentFixture(t, driver, launchAgentFixtureOptions{
		AgentFiles: map[string]string{
			launchAgentChildName: launchAgentChildAgentFile,
		},
	})

	result, err := fixture.runtime.Run(context.Background(), TurnRequest{
		Message:   "delegate an inspection",
		Workspace: fixture.workspace,
	})
	require.NoError(t, err)

	runs, err := fixture.store.ListModelRuns(
		context.Background(),
		result.SessionID,
		session.ListModelRunsOptions{Limit: 10},
	)
	require.NoError(t, err)
	require.Len(t, runs.Items, 2)
	root := findModelRunByStage(t, runs.Items, models.ModelRunStageTurn)
	child := findModelRunByStage(t, runs.Items, models.ModelRunStageChild)

	assert.Equal(t, runtimeTestModelReference, root.ModelReference)
	assert.Equal(t, "test", root.ConnectionName)
	assert.Equal(t, runtimeTestModelID, root.RequestedModelID)
	assert.Equal(t, models.ModelRunStateCompleted, root.State)
	assert.Equal(t, "root result", root.ResponseText)
	assert.Equal(t, "root reasoning", root.ResponseThinking)
	rootSettings := modelAuditSettings(t, root.RequestSettingsJSON)
	assert.Equal(t, true, rootSettings["autoToolCalls"])
	assert.Equal(t, true, rootSettings["streaming"])
	assert.Equal(t, float64(defaultMaxToolRounds), rootSettings["maxRounds"])
	assert.Contains(t, root.ResponseMessagesJSON, "root result")

	rootCalls, err := fixture.store.ListModelCalls(
		context.Background(),
		result.SessionID,
		root.ID,
		session.ListModelCallsOptions{Limit: 10},
	)
	require.NoError(t, err)
	require.Len(t, rootCalls.Items, 2)
	assert.Equal(t, []int64{0, 1}, modelAuditRounds(rootCalls.Items))
	assert.Contains(t, rootCalls.Items[0].RequestToolsJSON, toolNameLaunchAgent)
	assert.Contains(t, rootCalls.Items[1].RequestMessagesJSON, launchAgentCallID)
	assert.Equal(t, models.ModelCallStateCompleted, rootCalls.Items[1].State)
	assert.False(t, rootCalls.Items[1].CostKnown)

	assert.NotNil(t, child.AgentRunID)
	assert.Equal(t, models.ModelRunStateCompleted, child.State)
	assert.Equal(t, "child result", child.ResponseText)
	assert.Equal(t, "child reasoning", child.ResponseThinking)
	childCalls, err := fixture.store.ListModelCalls(
		context.Background(),
		result.SessionID,
		child.ID,
		session.ListModelCallsOptions{Limit: 10},
	)
	require.NoError(t, err)
	require.Len(t, childCalls.Items, 1)
	assert.Contains(t, childCalls.Items[0].RequestMessagesJSON, "inspect this workspace")
	assert.Contains(t, childCalls.Items[0].ResponseMessageJSON, "child result")
}

func TestModelAuditRecorderPersistsRetryTokenAndCostBreakdown(t *testing.T) {
	ctx := context.Background()
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver())
	opened, err := fixture.store.CreateOrResume(ctx, nil, session.OpenSessionOptions{
		RootAgent: runtimeTestAgentName,
		ModelID:   modelAuditTestReference,
	})
	require.NoError(t, err)
	lease, err := fixture.store.AcquireTurn(ctx, opened.Session.ID, session.StartTurnInput{
		RequestID: uuid.New(),
		Workspace: fixture.workspace,
		Messages: []session.MessageInput{{
			Role:    models.MessageRoleUser,
			Content: "inspect the audit record",
		}},
	})
	require.NoError(t, err)

	model := elelem.Model{
		ID: modelAuditTestModelID,
		Pricing: elelem.ModelPricing{
			InputPerToken:  0.00001,
			OutputPerToken: 0.00002,
		},
	}
	settings, err := newModelAuditSettings(
		model,
		true,
		7,
		4096,
		1024,
		3,
		15*time.Second,
		2048,
		2*time.Minute,
	)
	require.NoError(t, err)
	audit, err := newModelAuditRecorder(ctx, modelAuditOptions{
		Store:               fixture.store,
		SessionID:           opened.Session.ID,
		TurnID:              lease.TurnID,
		Stage:               models.ModelRunStageTurn,
		ModelReference:      modelAuditTestReference,
		Model:               model,
		RequestSettingsJSON: settings,
		Now:                 time.Now,
	})
	require.NoError(t, err)

	requestMessages := []elelem.Message{{
		Role:    elelem.RoleUser,
		Content: elelem.Text("inspect the audit record"),
	}}
	require.NoError(t, audit.onRoundStart(ctx, &elelem.RoundEvent{
		Round:    0,
		Messages: requestMessages,
		Tools: []elelem.Tool{{
			Name:            "read_file",
			Description:     "Read one file.",
			ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
			StrictArguments: true,
			Timeout:         15 * time.Second,
		}},
	}))

	retry := elelem.RetryAttempt{
		Attempt:  1,
		Reason:   elelem.RetryReasonRateLimited,
		Err:      ctxerrors.New("provider rate limited"),
		Status:   429,
		Delay:    25 * time.Millisecond,
		Streamed: true,
		Tokens: elelem.TokenCounts{
			Prompt:     3,
			Completion: 2,
			Total:      5,
		},
	}
	require.NoError(t, audit.onRetry(ctx, retry))
	assistantMessage := elelem.Message{
		Role:      elelem.RoleAssistant,
		Content:   elelem.Text("audit complete"),
		Reasoning: "checked every counter",
	}
	require.NoError(t, audit.onAssistantMessage(ctx, assistantMessage))
	usage := elelem.Usage{
		TokenCounts: elelem.TokenCounts{
			Prompt:            10,
			Completion:        5,
			Total:             15,
			Reasoning:         4,
			CacheRead:         2,
			CacheWrite:        1,
			CacheWriteLongTTL: 1,
		},
		Model:        "audit-model-served",
		FinishReason: elelem.FinishReasonStop,
		Retry: elelem.RetryInfo{
			TotalAttempts:          2,
			FailedAttempts:         []elelem.RetryAttempt{retry},
			WastedPromptTokens:     3,
			WastedCompletionTokens: 2,
			WastedTotalTokens:      5,
		},
	}
	require.NoError(t, audit.onRoundEnd(ctx, &elelem.RoundEvent{
		Round: 0,
		Usage: usage,
	}))
	response := &elelem.Response{
		Text:         "audit complete",
		Reasoning:    "checked every counter",
		Messages:     []elelem.Message{assistantMessage},
		Usage:        usage,
		Cost:         model.Cost(usage),
		Model:        "audit-model-served",
		FinishReason: elelem.FinishReasonStop,
	}
	require.NoError(t, audit.finish(ctx, response, nil))

	runs, err := fixture.store.ListModelRuns(
		ctx,
		opened.Session.ID,
		session.ListModelRunsOptions{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	run := runs.Items[0]
	assert.Equal(t, "gateway", run.ConnectionName)
	assert.Equal(t, modelAuditTestModelID, run.RequestedModelID)
	assert.Equal(t, "audit-model-served", run.ResponseModelID)
	assert.Equal(t, "audit complete", run.ResponseText)
	assert.Equal(t, "checked every counter", run.ResponseThinking)
	assert.Equal(t, "0.0002", run.ResponseCostAmount)
	assert.Equal(t, "0.00007", run.RetryCostAmount)
	assert.Equal(t, "0.00027", run.BilledCostAmount)
	assert.True(t, run.CostKnown)
	assert.Contains(t, run.RequestSettingsJSON, `"maxOutputTokens":1024`)
	assert.Equal(t, "[]", run.ResponseInjectionsJSON)

	calls, err := fixture.store.ListModelCalls(
		ctx,
		opened.Session.ID,
		run.ID,
		session.ListModelCallsOptions{Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, calls.Items, 1)
	call := calls.Items[0]
	assert.Equal(t, int64(10), call.PromptTokens)
	assert.Equal(t, int64(5), call.CompletionTokens)
	assert.Equal(t, int64(4), call.ReasoningTokens)
	assert.Equal(t, int64(2), call.CacheReadTokens)
	assert.Equal(t, int64(1), call.CacheWriteTokens)
	assert.Equal(t, int64(1), call.CacheWriteLongTTLTokens)
	assert.Equal(t, int64(3), call.WastedPromptTokens)
	assert.Equal(t, int64(2), call.WastedCompletionTokens)
	assert.Equal(t, int64(5), call.WastedTotalTokens)
	assert.Equal(t, int64(2), call.TotalAttempts)
	assert.Equal(t, int64(1), call.RetryAttemptCount)
	assert.Equal(t, "0.00027", call.BilledCostAmount)
	assert.True(t, call.CostKnown)
	assert.Contains(t, call.RequestMessagesJSON, "inspect the audit record")
	assert.Contains(t, call.RequestToolsJSON, "read_file")
	assert.Contains(t, call.RetryAttemptsJSON, "provider rate limited")
	assert.Contains(t, call.ResponseUsageJSON, "cacheWriteLongTtl")
}

func TestModelAuditJSONArrayNormalizesNilSlices(t *testing.T) {
	encoded, err := modelAuditJSONArray([]string(nil), "test values")
	require.NoError(t, err)
	assert.Equal(t, "[]", encoded)

	_, err = modelAuditJSONArray(map[string]any{}, "test values")
	require.Error(t, err)
}

func TestModelAuditReplayNormalizesLegacyNullArrays(t *testing.T) {
	run, err := modelRunToAPI(&models.ModelRun{
		RequestSettingsJSON:    "{}",
		ResponseMessagesJSON:   modelAuditNullJSON,
		ResponseInjectionsJSON: modelAuditNullJSON,
		ResponseUsageJSON:      "{}",
	})
	require.NoError(t, err)
	assert.Empty(t, run.ResponseMessages)
	assert.Empty(t, run.ResponseInjections)

	call, err := modelCallToAPI(&models.ModelCall{
		RequestMessagesJSON: modelAuditNullJSON,
		RequestToolsJSON:    modelAuditNullJSON,
		ResponseMessageJSON: modelAuditNullJSON,
		ResponseUsageJSON:   "{}",
		RetryAttemptsJSON:   modelAuditNullJSON,
	})
	require.NoError(t, err)
	assert.Empty(t, call.RequestMessages)
	assert.Empty(t, call.RequestTools)
	assert.Empty(t, call.RetryAttempts)
}

func findModelRunByStage(
	t *testing.T,
	runs []*models.ModelRun,
	stage models.ModelRunStage,
) *models.ModelRun {
	t.Helper()
	for _, run := range runs {
		if run.Stage == stage {
			return run
		}
	}

	t.Fatalf("model run stage %q was not persisted", stage)

	return nil
}

func modelAuditRounds(calls []*models.ModelCall) []int64 {
	rounds := make([]int64, 0, len(calls))
	for _, call := range calls {
		rounds = append(rounds, call.Round)
	}

	return rounds
}

func modelAuditSettings(t *testing.T, encoded string) map[string]any {
	t.Helper()

	settings := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(encoded), &settings))

	return settings
}
