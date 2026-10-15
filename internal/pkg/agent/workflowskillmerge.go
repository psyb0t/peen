package agent

import (
	"slices"

	"github.com/psyb0t/ctxerrors"
)

func mergeWorkflowSkills(
	current, next *workflowSkillState,
) (*workflowSkillState, error) {
	ledger, err := restoreWorkflowSkills(next)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore next skill state")
	}

	previous, err := restoreWorkflowSkills(current)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "restore previous skill state")
	}

	ledger.record(previous.workflowState().Delivered...)
	ledger.compactedThrough = max(ledger.compactedThrough,
		previous.compactedThrough)
	entries := make(map[int64][]string)

	for _, state := range []*workflowSkillState{
		previous.workflowState(), ledger.workflowState(),
	} {
		for _, entry := range state.History {
			if entry.Sequence <= ledger.compactedThrough {
				continue
			}

			entries[entry.Sequence] = append(entries[entry.Sequence],
				entry.Names...)
		}
	}

	ledger.restoredHistory = nil

	for sequence, names := range entries {
		slices.Sort(names)
		ledger.restoredHistory = append(ledger.restoredHistory,
			workflowSkillHistory{
				Sequence: sequence, Names: slices.Compact(names),
			})
	}

	return ledger.workflowState(), nil
}
