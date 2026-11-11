package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/psyb0t/ctxerrors"
)

const compactToolFirstAttempt = 1

func (p *preparedTurn) recordCompactRootStarts(
	ctx context.Context,
	input workflowRoundInput,
	result *workflowRoundResult,
) error {
	return prepareCompactToolStarts(ctx, input, result,
		func(tool workflowToolInput) error {
			p.attachPublisher(ctx)

			return p.startWorkflowToolPhase(ctx, workflowToolPhaseInput{
				Tool: tool, State: workflowToolPhaseState{
					StreamAttempt: compactToolFirstAttempt,
				},
			})
		})
}

func (s *agentRunSink) recordCompactChildStarts(
	ctx context.Context,
	input workflowRoundInput,
	result *workflowRoundResult,
) error {
	return prepareCompactToolStarts(ctx, input, result,
		func(tool workflowToolInput) error {
			return s.onToolCallStart(ctx, workflowPhaseCallEvent(tool))
		})
}

// Starts share the model receipt, so Stop can close a call even when the
// handler never commits. The recorded times and stream attempt also let an
// inspected handler result proceed straight to projection after a restart.
func prepareCompactToolStarts(
	ctx context.Context,
	input workflowRoundInput,
	result *workflowRoundResult,
	record func(workflowToolInput) error,
) error {
	if input.Round == input.Execution.MaxRounds-1 {
		return nil
	}

	var conversation workflowConversation
	if err := json.Unmarshal(result.Context, &conversation); err != nil {
		return ctxerrors.Wrap(err, "decode compact tool start context")
	}

	conversation.ToolStarts = nil
	input.Execution.Context = result.Context

	for index, call := range result.Calls {
		if err := ctx.Err(); err != nil {
			return ctxerrors.Wrap(err, "prepare compact tool start canceled")
		}

		tool := workflowToolInput{Round: input, Call: call, Index: index}
		tool.Compact = compactWorkflowTool(tool)

		if !tool.Compact {
			continue
		}

		if err := record(tool); err != nil {
			return ctxerrors.Wrap(err, "buffer compact tool start")
		}

		if conversation.ToolStarts == nil {
			conversation.ToolStarts = make(map[string]time.Time)
		}

		conversation.ToolStarts[call.ID] = time.Now().UTC()
	}

	encoded, err := json.Marshal(conversation)
	if err != nil {
		return ctxerrors.Wrap(err, "encode compact tool start context")
	}

	result.Context = encoded

	return nil
}

func initialWorkflowToolState(
	tool workflowToolInput,
) (workflowToolPhaseState, error) {
	state := workflowToolPhaseState{}
	if !tool.Compact {
		return state, nil
	}

	var conversation workflowConversation

	err := json.Unmarshal(tool.Round.Execution.Context, &conversation)
	if err != nil {
		return state, ctxerrors.Wrap(err, "decode recorded tool start")
	}

	state.StartedAt = conversation.ToolStarts[tool.Call.ID]
	state.StreamAttempt = compactToolFirstAttempt
	state.Event = &workflowToolEventState{
		RawArguments: bytes.Clone(tool.Call.Arguments),
	}

	return state, nil
}

func startsToolInPhase(input workflowToolPhaseInput) bool {
	return input.State.LastPhase == "" &&
		(!input.Tool.Compact || input.State.StartedAt.IsZero())
}
