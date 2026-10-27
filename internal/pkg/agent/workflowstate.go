package agent

import (
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/tools"
)

func mergeWorkflowDirectoryPrompt(
	current *workflowConversation,
	next workflowConversation,
) {
	if next.Runtime == nil || next.SystemPrompt == "" {
		return
	}

	if current.Runtime == nil ||
		current.Runtime.Directory != next.Runtime.Directory {
		current.SystemPrompt = next.SystemPrompt
	}
}

func mergeWorkflowRuntime(
	current, next *workflowRuntimeState,
) (*workflowRuntimeState, error) {
	if next == nil {
		return current, nil
	}

	merged := &workflowRuntimeState{
		Directory: next.Directory, Harness: slices.Clone(next.Harness),
	}

	if err := mergeRuntimeSkills(current, next, merged); err != nil {
		return nil, ctxerrors.Wrap(err, "merge runtime skills")
	}

	observations := make(map[string]tools.ObservedPath)

	if current != nil {
		for _, entry := range current.Observations {
			observations[entry.Path] = entry
		}
	}

	for _, entry := range next.Observations {
		mergeWorkflowObservation(observations, entry)
	}

	merged.Observations = make([]tools.ObservedPath, 0, len(observations))
	for _, entry := range observations {
		merged.Observations = append(merged.Observations, entry)
	}

	slices.SortFunc(merged.Observations, func(
		left, right tools.ObservedPath,
	) int {
		return strings.Compare(left.Path, right.Path)
	})

	return merged, nil
}

func resetWorkflowDirectorySkills(
	state *workflowRuntimeState,
	call workflowToolInput,
	result workflowToolResult,
) {
	if state == nil || call.Call.Name != toolNameChangeDirectory ||
		result.Result.IsError {
		return
	}

	state.Skills = nil
}

func mergeRuntimeSkills(
	current, next, merged *workflowRuntimeState,
) error {
	var previousSkills *workflowSkillState
	if current != nil && current.Directory == next.Directory {
		previousSkills = current.Skills
	}

	if previousSkills == nil && next.Skills == nil {
		return nil
	}

	state, err := mergeWorkflowSkills(previousSkills, next.Skills)
	if err != nil {
		return ctxerrors.Wrap(err, "merge delivered skills")
	}

	merged.Skills = state

	return nil
}

// mergeWorkflowObservation keeps the latest read of each path.
//
// Parallel tools run on forks of one executor, so two of them that read the
// same path record the same revision. The tie goes to the read with a content
// hash, as in the executor itself, and between two hashes to the smaller one.
// Either hash is content the model read, and a later change must still match
// the file, so the choice grants no access. The result does not depend on the
// order the tool results are merged in.
func mergeWorkflowObservation(
	observations map[string]tools.ObservedPath,
	entry tools.ObservedPath,
) {
	previous, exists := observations[entry.Path]
	if exists && previous.Revision > entry.Revision {
		return
	}

	if exists && previous.Revision == entry.Revision &&
		!winsObservationTie(entry, previous) {
		return
	}

	observations[entry.Path] = entry
}

func winsObservationTie(candidate, current tools.ObservedPath) bool {
	if candidate.HasHash != current.HasHash {
		return candidate.HasHash
	}

	return candidate.Hash < current.Hash
}
