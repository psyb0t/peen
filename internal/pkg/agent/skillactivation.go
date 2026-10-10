package agent

import (
	"errors"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

// requireKnownSkillReferences refuses text that names a skill the effective
// harness does not have. The failure is a validation failure that lists the
// effective skills, so a person or a model can correct the name.
func requireKnownSkillReferences(
	snapshot harness.Snapshot,
	text string,
) error {
	for _, name := range harness.SkillReferences(text) {
		if _, err := snapshot.ActivateSkill(name); err != nil {
			return unknownSkillReferenceError(snapshot, name)
		}
	}

	return nil
}

func unknownSkillReferenceError(
	snapshot harness.Snapshot,
	name string,
) error {
	return ctxerrors.Wrap(
		errors.Join(
			commerr.ErrValidationFailed,
			harness.NewUnknownSkillError(name, skillNames(snapshot)),
		),
		"resolve skill reference",
	)
}
