package agent

import (
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// reconstructionUnit links one indivisible group of assembled prompt messages
// back to the durable rows that produced it.
//
// An assistant message carrying tool calls and its contiguous tool results are
// one unit. Compacting half of one leaves a tool_call_id unanswered, which the
// provider rejects on a LATER request rather than at the call site, so the
// boundary has to be decided here rather than by whatever counts tokens.
type reconstructionUnit struct {
	FromMessageID uuid.UUID
	ToMessageID   uuid.UUID
	FromSequence  int64
	ToSequence    int64
	TurnID        uuid.UUID

	// MessageCount is how many assembled messages this unit contributes. The
	// durable-to-Elelem conversion is one to one, so it is also how many rows
	// the unit covers.
	MessageCount int

	// Roles is what the assembled messages must still look like when the limit
	// fires. It is a check, never a way to find a boundary.
	Roles []elelem.Role
}

// reconstructionPlan positions the durable units inside one assembled Elelem
// message slice.
//
// Compaction boundaries come from this plan, never from message text or a
// slice offset guessed when the limit fires: Elelem may reshape the transcript
// between assembly and the hook, and a boundary derived from content would
// then name the wrong database row.
type reconstructionPlan struct {
	// LeadCount is the index of the first unit's first message: past the
	// leading system message when there is one, and past the synthetic summary
	// message when a compaction is already active.
	LeadCount int

	// HasSummary reports whether the message directly before LeadCount is the
	// synthetic prior-conversation summary. A later compaction replaces that
	// message rather than adding a second one.
	HasSummary bool

	Units []reconstructionUnit
}

// buildReconstructionPlan groups completed history into whole units and
// records where they sit in the assembled prompt.
func buildReconstructionPlan(
	leadCount int,
	hasSummary bool,
	messages []*models.Message,
) (reconstructionPlan, error) {
	plan := reconstructionPlan{
		LeadCount:  leadCount,
		HasSummary: hasSummary,
		Units:      make([]reconstructionUnit, 0, len(messages)),
	}

	for _, message := range messages {
		if message == nil {
			return reconstructionPlan{}, ctxerrors.Wrap(
				ErrCompactionPlanMismatch,
				"nil history message",
			)
		}

		role, err := elelemRole(message.Role)
		if err != nil {
			return reconstructionPlan{}, ctxerrors.Wrap(
				err,
				"classify history message role",
			)
		}

		// A tool result belongs to the call that asked for it, so it extends
		// the open unit instead of starting one.
		if role == elelem.RoleTool && len(plan.Units) > 0 {
			plan.extendLastUnit(message, role)

			continue
		}

		plan.Units = append(plan.Units, reconstructionUnit{
			FromMessageID: message.ID,
			ToMessageID:   message.ID,
			FromSequence:  message.Sequence,
			ToSequence:    message.Sequence,
			TurnID:        message.TurnID,
			MessageCount:  1,
			Roles:         []elelem.Role{role},
		})
	}

	return plan, nil
}

func (p *reconstructionPlan) extendLastUnit(
	message *models.Message,
	role elelem.Role,
) {
	unit := &p.Units[len(p.Units)-1]
	unit.ToMessageID = message.ID
	unit.ToSequence = message.Sequence
	unit.MessageCount++
	unit.Roles = append(unit.Roles, role)
}

// verify reports whether the assembled slice still matches the plan.
//
// It never repairs a mismatch. A plan that no longer describes the transcript
// cannot name a database range, and guessing one would store a summary
// claiming to cover messages it never read.
func (p reconstructionPlan) verify(messages []elelem.Message) error {
	if p.HasSummary && p.LeadCount == 0 {
		return ctxerrors.Wrap(
			ErrCompactionPlanMismatch,
			"summary recorded with no room before the first unit",
		)
	}

	index := p.LeadCount

	for _, unit := range p.Units {
		if index+unit.MessageCount > len(messages) {
			return ctxerrors.Wrapf(
				ErrCompactionPlanMismatch,
				"plan needs %d messages, transcript has %d",
				index+unit.MessageCount,
				len(messages),
			)
		}

		if err := verifyUnitRoles(messages, index, unit); err != nil {
			return err
		}

		index += unit.MessageCount
	}

	return nil
}

func verifyUnitRoles(
	messages []elelem.Message,
	index int,
	unit reconstructionUnit,
) error {
	for offset, role := range unit.Roles {
		if messages[index+offset].Role == role {
			continue
		}

		return ctxerrors.Wrapf(
			ErrCompactionPlanMismatch,
			"message %d is %q, the plan expects %q",
			index+offset,
			messages[index+offset].Role,
			role,
		)
	}

	return nil
}

// summaryStart is where the synthetic summary message belongs: over the active
// one when there is one, otherwise directly after the leading system message.
func (p reconstructionPlan) summaryStart() int {
	if p.HasSummary {
		return p.LeadCount - 1
	}

	return p.LeadCount
}

// messageCount reports how many assembled messages the first units cover.
func (p reconstructionPlan) messageCount(units int) int {
	total := 0
	for _, unit := range p.Units[:units] {
		total += unit.MessageCount
	}

	return total
}

// coverableUnits reports how many leading units compaction may take.
//
// The newest completed turn stays raw no matter how far over budget the
// request is. Summarizing everything leaves the model with no verbatim record
// of what it just did, which is where a compacting agent starts repeating
// finished work.
func (p reconstructionPlan) coverableUnits() int {
	if len(p.Units) == 0 {
		return 0
	}

	newest := p.Units[len(p.Units)-1].TurnID

	count := len(p.Units)
	for count > 0 && p.Units[count-1].TurnID == newest {
		count--
	}

	return count
}

// advance records that the first units are now covered by a stored summary, so
// a second compaction in the same run plans against what is left.
func (p *reconstructionPlan) advance(units int) {
	p.LeadCount = p.summaryStart() + 1
	p.HasSummary = true
	p.Units = p.Units[units:]
}

// planLeadCount reports how many messages precede history in the assembled
// prompt: the system message when the prompt has one, plus the synthetic
// summary when the session carries an active compaction.
func planLeadCount(systemPrompt string, history *session.History) int {
	lead := 0
	if systemPrompt != "" {
		lead++
	}

	if history.Compaction != nil {
		lead++
	}

	return lead
}
