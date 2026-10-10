package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
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

	// ReferencedSkills are the skills the loaded skill names with :name,
	// and the skills those name in turn, depth first in reference order.
	// A skill the conversation already holds is left out.
	ReferencedSkills []referencedSkillOutput `json:"referencedSkills,omitempty"`
}

// referencedSkillOutput is one skill that came along with a requested one.
type referencedSkillOutput struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Content   string `json:"content"`
}

// skillHostTools registers the use_skill tool over one turn's resolved
// harness snapshot. Split from hostToolSet, like jobHostTools, to keep that
// builder under the function-length bound. The ledger records the skills the
// conversation holds; a nil ledger tracks nothing.
func skillHostTools(
	snapshot harness.Snapshot,
	ledger *skillLedger,
	onPostRun elelem.MessageInjector,
) []elelem.Tool {
	return []elelem.Tool{
		hostTool(
			toolNameUseSkill,
			useSkillDescription,
			useSkillSchema,
			useSkillHandler(snapshot, ledger),
			onPostRun,
		),
	}
}

// useSkillHandler closes over the turn's already-resolved snapshot, so this
// call and any repeat call for the same name in the same turn return
// identical content without re-resolving the harness.
//
// The requested skill is always returned in full. The skills it references
// come along unless the conversation already holds them, and everything
// returned is recorded so a later :name does not deliver it twice.
func useSkillHandler(
	snapshot harness.Snapshot,
	ledger *skillLedger,
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

		referenced, err := referencedSkills(snapshot, ledger, activated)
		if err != nil {
			return useSkillOutput{}, err
		}

		ctxscope.GetLogger(ctx).Info(
			"skill activated",
			"skill_name", activated.Name,
			"skill_hash", activated.Hash,
			"skill_source", activated.Source,
			"referenced_skills", len(referenced),
		)

		return useSkillOutput{
			Name:             activated.Name,
			Directory:        activated.Directory,
			Hash:             activated.Hash,
			Content:          activated.Content,
			ReferencedSkills: referenced,
		}, nil
	}
}

// referencedSkills expands the skills activated names and records them with
// activated itself in the ledger.
func referencedSkills(
	snapshot harness.Snapshot,
	ledger *skillLedger,
	activated harness.ActivatedSkill,
) ([]referencedSkillOutput, error) {
	held := ledger.held()
	delete(held, activated.Name)

	deliveries, err := snapshot.ExpandSkills([]string{activated.Name}, held)
	if err != nil {
		return nil, ctxerrors.Wrap(
			errors.Join(commerr.ErrValidationFailed, err),
			"expand skill references",
		)
	}

	referenced := make([]referencedSkillOutput, 0, len(deliveries))

	for _, delivery := range deliveries {
		ledger.record(delivery.Skill.Name)

		if delivery.Skill.Name == activated.Name {
			continue
		}

		referenced = append(referenced, referencedSkillOutput{
			Name:      delivery.Skill.Name,
			Directory: delivery.Skill.Directory,
			Content:   delivery.Skill.Content,
		})
	}

	if len(referenced) == 0 {
		return nil, nil
	}

	return referenced, nil
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
