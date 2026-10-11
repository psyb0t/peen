package events

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/naming"
	"github.com/stretchr/testify/assert"
)

func TestPeenEventTypesAreCamelCase(t *testing.T) {
	t.Parallel()

	peenTypes := []Type{
		TypeJobExited,
		TypeJobSignalled,
		TypeJobFailed,
		TypeAgentFinished,
		TypeAgentFailed,
	}

	for _, eventType := range peenTypes {
		assert.True(t, naming.IsValid(eventType), "event type %q", eventType)
	}
}
