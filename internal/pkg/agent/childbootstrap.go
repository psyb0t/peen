package agent

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

const childWorkflowBootstrapVersion = 1

type workflowChildBootstrap struct {
	Version    int       `json:"version"`
	SessionID  uuid.UUID `json:"sessionId"`
	AgentRunID uuid.UUID `json:"agentRunId"`
	TurnID     uuid.UUID `json:"turnId"`
	RequestID  uuid.UUID `json:"requestId"`

	ParentAgentRunID *uuid.UUID `json:"parentAgentRunId,omitempty"`

	ParentToolCallID string                    `json:"parentToolCallId"`
	Name             string                    `json:"name"`
	Definition       models.AgentRunDefinition `json:"definition"`
	Depth            int64                     `json:"depth"`
	Task             string                    `json:"task"`
	Instructions     string                    `json:"instructions"`
	AllowedTools     []string                  `json:"allowedTools"`
	Workspace        string                    `json:"workspace"`
	ModelReference   string                    `json:"modelReference"`
	ModelID          string                    `json:"modelId"`
	SystemPrompt     string                    `json:"systemPrompt"`
	MaxRounds        int                       `json:"maxRounds"`
	CompletedRounds  int                       `json:"completedRounds"`
	Model            *elelem.Model             `json:"model,omitempty"`
}

func snapshotChildWorkflowBootstrap(
	run *models.AgentRun,
	maximumRounds, completedRounds int,
) (*workflowChildBootstrap, error) {
	if run == nil {
		return nil, ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child workflow launch record")
	}

	var allowedTools []string
	if err := json.Unmarshal([]byte(run.AllowedToolsJSON),
		&allowedTools); err != nil {
		return nil, ctxerrors.Wrap(err, "decode child launch tool choices")
	}

	saved := &workflowChildBootstrap{
		Version: childWorkflowBootstrapVersion, SessionID: run.SessionID,
		AgentRunID: run.ID, TurnID: run.ParentTurnID, RequestID: run.RequestID,
		ParentToolCallID: run.ParentToolCallID,
		Name:             run.Name,
		Definition:       run.Definition, Depth: run.Depth,
		Task: run.Task, Instructions: run.Instructions,
		AllowedTools: allowedTools, Workspace: run.Workspace,
		ModelReference: run.ModelReference, ModelID: run.ModelID,
		SystemPrompt: run.SystemPrompt,
		MaxRounds:    maximumRounds, CompletedRounds: completedRounds,
	}
	if run.ParentAgentRunID != nil {
		parent := *run.ParentAgentRunID
		saved.ParentAgentRunID = &parent
	}

	if err := saved.validate(); err != nil {
		return nil, ctxerrors.Wrap(err, "validate child launch snapshot")
	}

	return saved, nil
}

func (b *workflowChildBootstrap) validate() error {
	if b == nil {
		return ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"saved child launch")
	}

	if b.Version != childWorkflowBootstrapVersion ||
		invalidChildBootstrapIdentity(b) || invalidChildBootstrapBudget(b) ||
		invalidChildBootstrapText(b) || invalidChildBootstrapDefinition(b) {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"invalid saved child launch")
	}

	if b.Model != nil && b.Model.ID != b.ModelID {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"saved child model metadata identity changed")
	}

	return nil
}

func invalidChildBootstrapIdentity(b *workflowChildBootstrap) bool {
	return b.SessionID == uuid.Nil || b.AgentRunID == uuid.Nil ||
		b.TurnID == uuid.Nil || b.RequestID == uuid.Nil || b.Depth < 1 ||
		(b.ParentAgentRunID != nil &&
			(*b.ParentAgentRunID == uuid.Nil ||
				*b.ParentAgentRunID == b.AgentRunID))
}

func invalidChildBootstrapBudget(b *workflowChildBootstrap) bool {
	return b.MaxRounds < 1 || b.CompletedRounds < 0 ||
		b.CompletedRounds >= b.MaxRounds
}

func invalidChildBootstrapText(b *workflowChildBootstrap) bool {
	values := []string{
		b.Name, b.Task, b.Instructions, b.Workspace,
		b.ModelReference, b.ModelID, b.SystemPrompt,
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}

	return false
}

func invalidChildBootstrapDefinition(b *workflowChildBootstrap) bool {
	return b.Definition != models.AgentRunDefinitionStored &&
		b.Definition != models.AgentRunDefinitionAdHoc
}

func (b *workflowChildBootstrap) validateAgainst(run *models.AgentRun) error {
	if err := b.validate(); err != nil {
		return ctxerrors.Wrap(err, "validate retained child launch")
	}

	if run == nil || childBootstrapIdentityChanged(b, run) ||
		childBootstrapDefinitionChanged(b, run) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"saved child launch differs from its run")
	}

	var tools []string
	if err := json.Unmarshal([]byte(run.AllowedToolsJSON), &tools); err != nil {
		return ctxerrors.Wrap(err, "decode retained child tool choices")
	}

	if !slices.Equal(b.AllowedTools, tools) {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"saved child tool choices changed")
	}

	return nil
}

func childBootstrapIdentityChanged(
	b *workflowChildBootstrap,
	run *models.AgentRun,
) bool {
	return b.SessionID != run.SessionID || b.AgentRunID != run.ID ||
		b.TurnID != run.ParentTurnID || b.RequestID != run.RequestID ||
		b.ParentToolCallID != run.ParentToolCallID || b.Depth != run.Depth ||
		!sameChildBootstrapParent(b.ParentAgentRunID, run.ParentAgentRunID)
}

func sameChildBootstrapParent(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}

	return *left == *right
}

func childBootstrapDefinitionChanged(
	b *workflowChildBootstrap,
	run *models.AgentRun,
) bool {
	return b.Name != run.Name || b.Task != run.Task ||
		b.Instructions != run.Instructions || b.Definition != run.Definition ||
		b.ModelReference != run.ModelReference || b.ModelID != run.ModelID
}

func (b *workflowChildBootstrap) validateRound(input workflowRoundInput) error {
	if err := b.validate(); err != nil {
		return ctxerrors.Wrap(err, "validate child round launch")
	}

	if input.Execution.SessionID != b.SessionID {
		return ctxerrors.Wrap(commerr.ErrPermissionDenied,
			"child round belongs to another session")
	}

	remaining := b.MaxRounds - b.CompletedRounds
	if input.Execution.MaxRounds != remaining ||
		input.Round < 0 || input.Round >= remaining {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"child workflow round budget changed")
	}

	return nil
}

func (b *workflowChildBootstrap) definition() childDefinition {
	return childDefinition{
		name: b.Name, instructions: b.Instructions,
		kind: string(b.Definition), allowedTools: slices.Clone(b.AllowedTools),
	}
}

func validateWorkflowChildConversation(
	conversation workflowConversation,
	input workflowRoundInput,
) error {
	if conversation.Child == nil {
		return nil
	}

	if conversation.Root != nil ||
		conversation.ModelReference != conversation.Child.ModelReference {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"child workflow conversation identity changed")
	}

	return conversation.Child.validateRound(input)
}
