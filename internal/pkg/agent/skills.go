package agent

import (
	"context"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

const (
	skillNameSeparator = ", "
	skillsNoneMessage  = "no skills are available for this workspace"
)

// useSkillInput selects one effective skill by its resolver-assigned name,
// exactly as it appears in the turn's skill catalogue.
type useSkillInput struct {
	Name string `json:"name"`
}

// useSkillOutput is one skill's complete content plus enough provenance for
// the model to read sibling files afterward with read_file.
type useSkillOutput struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Hash      string `json:"hash"`
	Content   string `json:"content"`
}

// skillHostTools registers the use_skill tool over one turn's resolved
// harness snapshot. Split from hostToolSet, like jobHostTools, to keep that
// builder under the function-length bound.
func skillHostTools(
	snapshot harness.Snapshot,
	onPostRun elelem.MessageInjector,
) []elelem.Tool {
	return []elelem.Tool{
		hostTool(
			toolNameUseSkill,
			useSkillDescription,
			useSkillSchema,
			useSkillHandler(snapshot),
			onPostRun,
		),
	}
}

// useSkillHandler closes over the turn's already-resolved snapshot, so this
// call and any repeat call for the same name in the same turn return
// identical content without re-resolving the harness.
func useSkillHandler(
	snapshot harness.Snapshot,
) func(context.Context, useSkillInput) (useSkillOutput, error) {
	return func(
		ctx context.Context,
		input useSkillInput,
	) (useSkillOutput, error) {
		activated, err := snapshot.ActivateSkill(input.Name)
		if err != nil {
			return useSkillOutput{}, unknownSkillError(
				snapshot,
				input.Name,
				err,
			)
		}

		ctxscope.GetLogger(ctx).Info(
			"skill activated",
			"skill_name", activated.Name,
			"skill_hash", activated.Hash,
			"skill_source", activated.Source,
		)

		return useSkillOutput{
			Name:      activated.Name,
			Directory: activated.Directory,
			Hash:      activated.Hash,
			Content:   activated.Content,
		}, nil
	}
}

// unknownSkillError names the currently effective skills so the model can
// correct its own call instead of the turn ending on a bad name.
func unknownSkillError(
	snapshot harness.Snapshot,
	name string,
	cause error,
) error {
	available := skillsNoneMessage

	names := skillNames(snapshot)
	if len(names) > 0 {
		available = "available skills: " +
			strings.Join(names, skillNameSeparator)
	}

	return ctxerrors.Wrapf(cause, "skill %q not found, %s", name, available)
}

func skillNames(snapshot harness.Snapshot) []string {
	skills := snapshot.Skills()

	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}

	return names
}
