package agent

import (
	"cmp"
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
)

type workflowSkillHistory struct {
	Sequence int64    `json:"sequence"`
	Names    []string `json:"names"`
}

type workflowSkillState struct {
	CompactedThrough int64                  `json:"compactedThrough"`
	History          []workflowSkillHistory `json:"history"`
	Delivered        []string               `json:"delivered"`
}

func (l *skillLedger) workflowState() *workflowSkillState {
	state := &workflowSkillState{}
	if l == nil {
		return state
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	state.CompactedThrough = l.compactedThrough

	for _, entry := range l.restoredHistory {
		state.History = append(state.History, workflowSkillHistory{
			Sequence: entry.Sequence, Names: slices.Clone(entry.Names),
		})
	}

	calls := useSkillCallIDs(l.history)
	for _, message := range l.history {
		names := workflowMessageSkillNames(message, calls)
		if len(names) != 0 {
			state.History = append(state.History, workflowSkillHistory{
				Sequence: message.Sequence, Names: names,
			})
		}
	}

	for name := range l.delivered {
		state.Delivered = append(state.Delivered, name)
	}

	slices.Sort(state.Delivered)
	slices.SortFunc(state.History, func(
		left, right workflowSkillHistory,
	) int {
		return cmp.Compare(left.Sequence, right.Sequence)
	})

	return state
}

func workflowMessageSkillNames(
	message *models.Message,
	calls map[string]struct{},
) []string {
	if message.InjectedKind == models.InjectedKindSkill {
		if name, found := skillMessageName(message.Content); found {
			return []string{name}
		}
	}

	if message.Role == models.MessageRoleTool {
		return useSkillResultNames(message, calls)
	}

	return nil
}

func restoreWorkflowSkills(state *workflowSkillState) (*skillLedger, error) {
	ledger := newSkillLedger()
	if state == nil {
		return ledger, nil
	}

	if state.CompactedThrough < 0 {
		return nil, ctxerrors.Wrap(commerr.ErrValidationFailed,
			"restored skill compaction sequence")
	}

	ledger.compactedThrough = state.CompactedThrough

	if err := validateWorkflowSkillNames(state.Delivered); err != nil {
		return nil, ctxerrors.Wrap(err, "validate saved skill deliveries")
	}

	ledger.record(state.Delivered...)

	seen := make(map[int64]struct{}, len(state.History))
	for _, entry := range state.History {
		if err := validateWorkflowSkillHistory(entry, state.CompactedThrough,
			seen); err != nil {
			return nil, ctxerrors.Wrap(err, "validate saved skill history")
		}

		seen[entry.Sequence] = struct{}{}

		ledger.restoredHistory = append(ledger.restoredHistory,
			workflowSkillHistory{
				Sequence: entry.Sequence, Names: slices.Clone(entry.Names),
			})
		for _, name := range entry.Names {
			ledger.historyNames[name] = struct{}{}
		}
	}

	return ledger, nil
}

func validateWorkflowSkillHistory(
	entry workflowSkillHistory,
	compactedThrough int64,
	seen map[int64]struct{},
) error {
	if entry.Sequence <= compactedThrough {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"restored skill history sequence")
	}

	if _, exists := seen[entry.Sequence]; exists {
		return ctxerrors.Wrap(commerr.ErrValidationFailed,
			"duplicate restored skill history sequence")
	}

	return validateWorkflowSkillNames(entry.Names)
}

func validateWorkflowSkillNames(names []string) error {
	for _, name := range names {
		if name == "" || strings.TrimSpace(name) != name {
			return ctxerrors.Wrap(commerr.ErrValidationFailed,
				"restored skill name")
		}
	}

	return nil
}

func (l *skillLedger) forgetRestoredSkillsThrough(sequence int64) {
	remaining := make([]workflowSkillHistory, 0, len(l.restoredHistory))
	for _, entry := range l.restoredHistory {
		if entry.Sequence <= sequence {
			continue
		}

		remaining = append(remaining, entry)
		for _, name := range entry.Names {
			l.historyNames[name] = struct{}{}
		}
	}

	l.restoredHistory = remaining
}
