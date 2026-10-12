package peen

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/hooks"
)

// HookInvocation carries the same lifecycle identity and input as file hooks.
type HookInvocation struct {
	Event          string          `json:"event"`
	SessionID      string          `json:"sessionId"`
	RequestID      string          `json:"requestId"`
	TurnID         string          `json:"turnId"`
	AgentRunID     *string         `json:"agentRunId,omitempty"`
	Tool           string          `json:"tool,omitempty"`
	CallID         string          `json:"callId,omitempty"`
	Workspace      string          `json:"workspace"`
	Paths          []string        `json:"paths,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	StateDirectory string          `json:"stateDirectory,omitempty"`
	ContextTokens  int             `json:"contextTokens"`
}

// HookOutcome denies, injects context or publishes external notice data.
// Returning an error stops the hook boundary. File-hook denials run first.
type HookOutcome struct {
	Deny       string
	Injections []string
	Events     []NoticeRequest
}

type HookHandler func(context.Context, HookInvocation) (HookOutcome, error)

func hookCallback(definitions map[string]HookHandler) (
	func(context.Context, hooks.Invocation) (hooks.Outcome, error), error,
) {
	callbacks := maps.Clone(definitions)
	for name, callback := range callbacks {
		if callback == nil || !slices.Contains(harness.HookEvents(),
			harness.HookEvent(name)) {
			return nil, ctxerrors.Wrap(
				commerr.ErrValidationFailed,
				"invalid Go hook event or callback",
			)
		}
	}

	return func(ctx context.Context, invocation hooks.Invocation) (
		hooks.Outcome, error,
	) {
		callback := callbacks[string(invocation.Event)]
		if callback == nil {
			return hooks.Outcome{}, nil
		}

		input, err := convertRecord[HookInvocation](invocation, nil)
		if err != nil {
			return hooks.Outcome{}, err
		}

		value, err := callback(ctx, input)
		if err != nil {
			return hooks.Outcome{}, ctxerrors.Wrap(err, "run embedding hook")
		}

		if value.Deny != "" {
			return hooks.Outcome{}, &hooks.DenialError{Reason: value.Deny}
		}

		result := hooks.Outcome{Injections: slices.Clone(value.Injections)}
		for _, notice := range value.Events {
			data, err := json.Marshal(notice.Data)
			if err != nil {
				return hooks.Outcome{}, ctxerrors.Wrap(
					err,
					"encode Go hook event",
				)
			}

			result.Events = append(result.Events, events.Notice{
				Type: notice.Type, Summary: notice.Summary,
				Data: data, Delivery: string(notice.Delivery),
			})
		}

		return result, nil
	}, nil
}

func denyVirtualCommand(context.Context, hooks.CommandInput) ([]byte, error) {
	return nil, ctxerrors.Wrap(
		commerr.ErrPermissionDenied,
		"virtual workspace requires a command runner",
	)
}
