package session

import (
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"gorm.io/gen/field"
)

func turnOutputAssignments(
	tx *repositories.Query,
	input FinalizeTurnInput,
) []field.AssignExpr {
	turn := tx.Turn

	var assignments []field.AssignExpr

	if len(input.Output) != 0 {
		assignments = append(assignments,
			turn.Output.Value(string(input.Output)))
	}

	if input.OutputError != "" {
		assignments = append(assignments,
			turn.OutputError.Value(input.OutputError))
	}

	return assignments
}
