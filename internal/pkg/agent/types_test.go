package agent

import (
	"testing"

	"github.com/psyb0t/peen/internal/pkg/naming"
	"github.com/stretchr/testify/assert"
)

func TestEventTypesAreCamelCase(t *testing.T) {
	t.Parallel()

	seen := map[string]struct{}{}

	for _, eventType := range EventTypes() {
		assert.True(t, naming.IsValid(eventType), "event type %q", eventType)
		assert.NotContains(t, seen, eventType, "duplicate event type")

		seen[eventType] = struct{}{}
	}

	assert.True(t, naming.IsValid(agentEventSource), "event source")
}
