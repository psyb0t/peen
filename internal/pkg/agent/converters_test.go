package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
