package agent

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const workflowCompactionStateVersion = 1

type workflowCompactionState struct {
	Version         int                `json:"version"`
	Plan            reconstructionPlan `json:"plan"`
	Head            *compactionHead    `json:"head,omitempty"`
	ModelReference  string             `json:"modelReference"`
	Prompt          string             `json:"prompt"`
	PromptHash      string             `json:"promptHash"`
	MaxOutputTokens int                `json:"maxOutputTokens"`
	Timeout         time.Duration      `json:"timeout"`
}

func snapshotWorkflowCompaction(source *compactor) *workflowCompactionState {
	if source == nil {
		return nil
	}

	state := &workflowCompactionState{
		Version:         workflowCompactionStateVersion,
		Plan:            cloneCompactionPlan(source.plan),
		Head:            cloneCompactionHead(source.active),
		ModelReference:  source.options.ModelReference,
		Prompt:          source.options.Prompt,
		PromptHash:      source.options.PromptHash,
		MaxOutputTokens: source.options.MaxOutputTokens,
		Timeout:         source.options.Timeout,
	}

	return state
}

func cloneCompactionPlan(plan reconstructionPlan) reconstructionPlan {
	plan.Units = slices.Clone(plan.Units)
	for index := range plan.Units {
		plan.Units[index].Roles = slices.Clone(plan.Units[index].Roles)
	}

	return plan
}

func cloneCompactionHead(head *compactionHead) *compactionHead {
	if head == nil {
		return nil
	}

	cloned := *head

	return &cloned
}

func (s *workflowCompactionState) validate(messages []elelem.Message) error {
	if s == nil {
		return nil
	}

	if s.Version != workflowCompactionStateVersion {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"saved compaction version")
	}

	if invalidWorkflowCompactionSettings(s) {
		return ctxerrors.Wrap(ErrInvalidCompactionOptions,
			"saved compaction settings")
	}

	if invalidWorkflowCompactionPosition(s, len(messages)) {
		return ctxerrors.Wrap(ErrCompactionPlanMismatch,
			"saved compaction history position")
	}

	if s.Head != nil && invalidWorkflowCompactionHead(s.Head) {
		return ctxerrors.Wrap(ErrCompactionPlanMismatch,
			"saved compaction predecessor")
	}

	return s.validateUnits(messages)
}

func invalidWorkflowCompactionPosition(
	state *workflowCompactionState,
	messageCount int,
) bool {
	return state.Plan.LeadCount < 0 || state.Plan.LeadCount > messageCount ||
		state.Plan.HasSummary != (state.Head != nil) ||
		(state.Plan.HasSummary && state.Plan.LeadCount == 0)
}

func invalidWorkflowCompactionSettings(s *workflowCompactionState) bool {
	return strings.TrimSpace(s.ModelReference) == "" ||
		strings.TrimSpace(s.Prompt) == "" ||
		strings.TrimSpace(s.PromptHash) == "" ||
		s.MaxOutputTokens <= 0 || s.Timeout <= 0
}

func invalidWorkflowCompactionHead(head *compactionHead) bool {
	return head.ID == uuid.Nil || head.FromMessageID == uuid.Nil ||
		head.FromSequence <= 0 || head.SourceMessageCount <= 0
}

func (s *workflowCompactionState) validateUnits(
	messages []elelem.Message,
) error {
	index := s.Plan.LeadCount

	var lastSequence int64

	if s.Head != nil {
		lastSequence = s.Head.FromSequence
	}

	for _, unit := range s.Plan.Units {
		if invalidWorkflowCompactionUnit(unit, lastSequence,
			len(messages)-index) {
			return ctxerrors.Wrap(ErrCompactionPlanMismatch,
				"saved compaction unit bounds")
		}

		if err := verifyUnitRoles(messages, index, unit); err != nil {
			return ctxerrors.Wrap(err, "validate saved compaction roles")
		}

		index += unit.MessageCount
		lastSequence = unit.ToSequence
	}

	return nil
}

func validateWorkflowCompactionConversation(
	conversation workflowConversation,
) error {
	if conversation.Compaction == nil {
		return nil
	}

	messages := make([]elelem.Message, 0, len(conversation.Messages)+1)
	if conversation.SystemPrompt != "" {
		messages = append(messages, elelem.Message{
			Role:    elelem.RoleSystem,
			Content: elelem.Text(conversation.SystemPrompt),
		})
	}

	messages = append(messages, conversation.Messages...)

	return conversation.Compaction.validate(messages)
}

// releaseDroppedCompactionPlan forgets the summarize plan after the model
// request left out its oldest units, which happens when no completed history
// could be summarized. The plan names messages by position, so after a drop
// it would refuse every later phase of the turn or, worse, name the wrong
// rows. The next turn builds a fresh plan from the stored messages.
func releaseDroppedCompactionPlan(
	ctx context.Context,
	conversation *workflowConversation,
) {
	if conversation.Compaction == nil || conversation.historyDropped == nil ||
		!*conversation.historyDropped {
		return
	}

	conversation.Compaction = nil

	runtimecontext.Logger(ctx).Warn(
		"model round dropped history the compaction plan names, "+
			"continuing the turn without summaries",
		"reason", compactionReasonPlanReleased,
	)
}

func invalidWorkflowCompactionUnit(
	unit reconstructionUnit,
	lastSequence int64,
	remainingMessages int,
) bool {
	return unit.FromMessageID == uuid.Nil || unit.ToMessageID == uuid.Nil ||
		unit.TurnID == uuid.Nil || unit.FromSequence <= lastSequence ||
		unit.ToSequence < unit.FromSequence || unit.MessageCount <= 0 ||
		unit.MessageCount > remainingMessages ||
		len(unit.Roles) != unit.MessageCount
}
