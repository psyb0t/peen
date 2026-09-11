package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageRequestToTurnRequest(t *testing.T) {
	requestID := uuid.New()
	sessionID := uuid.New()

	testCases := []struct {
		name    string
		request MessageRequest
		want    TurnRequest
		wantErr error
	}{
		{
			name:    "minimal request",
			request: MessageRequest{Message: "inspect"},
			want: TurnRequest{
				Message:   "inspect",
				RequestID: requestID,
				SessionID: &sessionID,
			},
		},
		{
			name: "request with explicit settings",
			request: MessageRequest{
				Message:   "inspect",
				Workspace: new("/workspace"),
				Model:     new("provider/model"),
				SystemPrompt: &MessageSystemPrompt{
					Mode:    PromptModeAppend,
					Content: "extra rules",
				},
			},
			want: TurnRequest{
				Message:          "inspect",
				Workspace:        "/workspace",
				Model:            "provider/model",
				SystemPrompt:     "extra rules",
				SystemPromptMode: PromptModeAppend,
				RequestID:        requestID,
				SessionID:        &sessionID,
			},
		},
		{
			name:    "blank message",
			request: MessageRequest{Message: " \t"},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "blank explicit workspace",
			request: MessageRequest{
				Message:   "inspect",
				Workspace: new(" \n"),
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "blank explicit model",
			request: MessageRequest{
				Message: "inspect",
				Model:   new(""),
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "blank system prompt content",
			request: MessageRequest{
				Message: "inspect",
				SystemPrompt: &MessageSystemPrompt{
					Mode:    PromptModeReplace,
					Content: "  ",
				},
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "unknown system prompt mode",
			request: MessageRequest{
				Message: "inspect",
				SystemPrompt: &MessageSystemPrompt{
					Mode:    PromptMode("unknown"),
					Content: "rules",
				},
			},
			wantErr: commerr.ErrValidationFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := messageRequestToTurnRequest(
				tc.request,
				&sessionID,
				requestID,
			)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRuntimeRunsAndListsMessages(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first response"),
		elelemtest.Text("second response"),
	)
	fixture := newRuntimeFixture(t, driver)

	first, err := fixture.runtime.RunMessage(
		context.Background(),
		MessageRequest{Message: "first request"},
		nil,
		uuid.New(),
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, "first response", first.Text)

	second, err := fixture.runtime.RunMessage(
		context.Background(),
		MessageRequest{Message: "second request"},
		&first.SessionID,
		uuid.New(),
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, first.SessionID, second.SessionID)
	assert.Equal(t, "second response", second.Text)

	limit := int32(10)
	page, err := fixture.runtime.ListMessages(
		context.Background(),
		api.ListMessagesParams{
			Limit:      &limit,
			XSessionID: first.SessionID,
		},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{
			"first request",
			"first response",
			"second request",
			"second response",
		},
		apiMessageContents(page.Items),
	)
	assert.False(t, page.HasMore)
	assert.Equal(t, int32(10), page.Limit)
}

func apiMessageContents(messages []api.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}

	return contents
}
