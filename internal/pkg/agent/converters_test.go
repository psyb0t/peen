package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The change-agent-space form shows where a session runs now, so the session
// the API returns carries its agent space. A session recorded before agent
// spaces existed reports none.
func TestSessionToAPIReportsTheAgentSpace(t *testing.T) {
	t.Parallel()

	withSpace := SessionToAPI(
		&models.Session{ID: uuid.New(), AgentSpace: "docker:ops"},
		false,
	)
	require.NotNil(t, withSpace.AgentSpace)
	assert.Equal(t, "docker:ops", *withSpace.AgentSpace)

	withoutSpace := SessionToAPI(&models.Session{ID: uuid.New()}, false)
	assert.Nil(t, withoutSpace.AgentSpace)
}

func TestMessageToAPILabelsInjectedSkillMessages(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		injected     bool
		kind         models.InjectedKind
		wantInjected bool
		wantKind     *api.MessageInjectedKind
	}{
		{name: "typed message"},
		{
			name:         "injected without a kind",
			injected:     true,
			wantInjected: true,
		},
		{
			name:         "injected skill",
			injected:     true,
			kind:         models.InjectedKindSkill,
			wantInjected: true,
			wantKind:     skillKindPointer(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			message, err := messageToAPI(&models.Message{
				ID:            uuid.New(),
				Role:          models.MessageRoleUser,
				Content:       "text",
				ToolCallsJSON: "[]",
				Injected:      tc.injected,
				InjectedKind:  tc.kind,
			})
			require.NoError(t, err)

			assert.Equal(t, tc.wantKind, message.InjectedKind)
			assert.Equal(t, tc.wantInjected, message.Injected != nil)
		})
	}
}

func skillKindPointer() *api.MessageInjectedKind {
	kind := api.MessageInjectedKindSkill

	return &kind
}
