package agent

import (
	"slices"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
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
		if err := mergeWorkflowObservation(observations, entry); err != nil {
			return nil, err
		}
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

func mergeWorkflowObservation(
	observations map[string]tools.ObservedPath,
	entry tools.ObservedPath,
) error {
	previous, exists := observations[entry.Path]
	if exists && previous.Revision > entry.Revision {
		return nil
	}

	if exists && previous.Revision == entry.Revision && previous != entry {
		return ctxerrors.Wrap(commerr.ErrConflict,
			"conflicting read observations have the same revision")
	}

	observations[entry.Path] = entry

	return nil
}
