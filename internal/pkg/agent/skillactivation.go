package agent

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/harness"
)

const explicitSkillMarker = ':'

// explicitSkillNames finds standalone :skill-name references in one user
// message. A reference is exact, not semantic: it starts a message or follows
// whitespace, and it ends at a normal token boundary.
func explicitSkillNames(message string) []string {
	names := make([]string, 0)

	for index := 0; index < len(message); index++ {
		if message[index] != explicitSkillMarker ||
			!isExplicitSkillStart(message, index) {
			continue
		}

		end := index + 1
		for end < len(message) && isSkillNameByte(message[end]) {
			end++
		}

		if end == index+1 || !isExplicitSkillBoundary(message, end) {
			continue
		}

		names = append(names, message[index+1:end])
		index = end - 1
	}

	return uniqueStringValues(names)
}

func isExplicitSkillStart(message string, index int) bool {
	if index == 0 {
		return true
	}

	previous, _ := utf8.DecodeLastRuneInString(message[:index])

	return unicode.IsSpace(previous)
}

func isSkillNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' || value == '-'
}

func isExplicitSkillBoundary(message string, index int) bool {
	if index == len(message) {
		return true
	}

	next, _ := utf8.DecodeRuneInString(message[index:])
	if unicode.IsSpace(next) {
		return true
	}

	return strings.ContainsRune(",.;!?)]}", next)
}

func uniqueStringValues(values []string) []string {
	unique := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}

		seen[value] = struct{}{}
		unique = append(unique, value)
	}

	return unique
}

func resolveExplicitSkillNames(
	snapshot harness.Snapshot,
	message string,
) ([]string, error) {
	names := explicitSkillNames(message)
	for _, name := range names {
		if _, err := snapshot.ActivateSkill(name); err != nil {
			return nil, ctxerrors.Wrapf(
				err,
				"resolve explicit skill reference :%s",
				name,
			)
		}
	}

	return names, nil
}

func resolveTurnExplicitSkills(
	ctx context.Context,
	snapshot harness.Snapshot,
	message string,
) ([]string, error) {
	names, err := resolveExplicitSkillNames(snapshot, message)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "resolve explicit skills")
	}

	if err := logExplicitSkillActivations(ctx, snapshot, names); err != nil {
		return nil, err
	}

	return names, nil
}

func logExplicitSkillActivations(
	ctx context.Context,
	snapshot harness.Snapshot,
	names []string,
) error {
	for _, name := range names {
		activated, err := snapshot.ActivateSkill(name)
		if err != nil {
			return ctxerrors.Wrapf(
				err,
				"resolve explicit skill %s for logging",
				name,
			)
		}

		ctxscope.GetLogger(ctx).Info(
			"skill explicitly activated",
			"skill_name", activated.Name,
			"skill_hash", activated.Hash,
			"skill_source", activated.Source,
		)
	}

	return nil
}
