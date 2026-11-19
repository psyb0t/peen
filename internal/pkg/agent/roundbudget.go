package agent

import (
	"slices"

	"github.com/psyb0t/elelem"
)

const (
	// roundBudgetWarningRounds is how many tool rounds, the current one
	// included, are left when the model is told its budget is running out.
	roundBudgetWarningRounds = 3

	//nolint:lll // One model-facing note.
	roundBudgetNote = "\n<note kind=\"roundBudget\">3 tool rounds remain in this turn, this one included. Finish the work or reach a point where it can stop, and say what is left.</note>"
)

// addRoundBudgetNote tells the model, once per turn, that only
// roundBudgetWarningRounds tool rounds remain. The note is added as a text
// part of the newest message instead of as a message of its own, because
// compaction and transcript checks map request messages to stored rows by
// position. It changes only the request copy: history is a copy, the message
// content is cloned before it grows, and savedHistory drops the part again.
func addRoundBudgetNote(history []elelem.Message, round, maxRounds int) {
	if maxRounds <= roundBudgetWarningRounds ||
		maxRounds-round != roundBudgetWarningRounds || len(history) == 0 {
		return
	}

	last := &history[len(history)-1]
	if last.Role != elelem.RoleTool && last.Role != elelem.RoleUser {
		return
	}

	last.Content = append(slices.Clone(last.Content),
		elelem.TextOf(roundBudgetNote))
}

// withoutRoundBudgetNote returns message without the round budget part
// addRoundBudgetNote added, so the saved conversation holds what the tool or
// user actually said.
func withoutRoundBudgetNote(message elelem.Message) elelem.Message {
	isNote := func(part elelem.Part) bool {
		return part.Type == elelem.PartTypeText && part.Text == roundBudgetNote
	}

	if !slices.ContainsFunc(message.Content, isNote) {
		return message
	}

	message.Content = slices.DeleteFunc(slices.Clone(message.Content), isNote)

	return message
}
