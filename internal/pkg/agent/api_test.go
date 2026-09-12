package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/session"
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

func TestRuntimeReadsDirectCompactionLinksAndParents(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Text("first response"),
		elelemtest.Text("second response"),
	))

	first, err := fixture.runtime.RunMessage(
		context.Background(),
		MessageRequest{Message: "first request"},
		nil,
		uuid.New(),
		nil,
	)
	require.NoError(t, err)
	_, err = fixture.runtime.RunMessage(
		context.Background(),
		MessageRequest{Message: "second request"},
		&first.SessionID,
		uuid.New(),
		nil,
	)
	require.NoError(t, err)

	stored, err := fixture.store.ListMessages(
		context.Background(),
		first.SessionID,
		session.ListMessagesOptions{Limit: 10, Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	require.Len(t, stored.Items, 4)

	firstCompaction, err := fixture.store.CreateCompaction(
		context.Background(),
		first.SessionID,
		session.CompactionInput{
			FromMessageID:      stored.Items[0].ID,
			ToMessageID:        stored.Items[1].ID,
			FromSequence:       stored.Items[0].Sequence,
			ToSequence:         stored.Items[1].Sequence,
			Summary:            "first compacted segment",
			SourceMessageCount: 2,
			ModelID:            "test-model",
			PromptHash:         "first-prompt",
		},
	)
	require.NoError(t, err)
	secondCompaction, err := fixture.store.CreateCompaction(
		context.Background(),
		first.SessionID,
		session.CompactionInput{
			FromMessageID:          stored.Items[0].ID,
			ToMessageID:            stored.Items[2].ID,
			FromSequence:           stored.Items[0].Sequence,
			ToSequence:             stored.Items[2].Sequence,
			Summary:                "second compacted segment",
			SourceMessageCount:     3,
			ModelID:                "test-model",
			PromptHash:             "second-prompt",
			SupersedesCompactionID: &firstCompaction.ID,
		},
	)
	require.NoError(t, err)

	limit := int32(10)
	page, err := fixture.runtime.ListMessages(context.Background(), api.ListMessagesParams{
		Limit:      &limit,
		XSessionID: first.SessionID,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 4)
	require.NotNil(t, page.Items[0].CompactionId)
	require.NotNil(t, page.Items[1].CompactionId)
	require.NotNil(t, page.Items[2].CompactionId)
	assert.Equal(t, firstCompaction.ID, *page.Items[0].CompactionId)
	assert.Equal(t, firstCompaction.ID, *page.Items[1].CompactionId)
	assert.Equal(t, secondCompaction.ID, *page.Items[2].CompactionId)
	assert.Nil(t, page.Items[3].CompactionId)

	compactions, err := fixture.runtime.ListSessionCompactions(
		context.Background(),
		api.ListSessionCompactionsParams{
			Limit:      &limit,
			XSessionID: first.SessionID,
		},
	)
	require.NoError(t, err)
	require.Len(t, compactions.Compactions, 2)
	assert.Equal(t, secondCompaction.ID, compactions.Compactions[0].Id)
	require.NotNil(t, compactions.Compactions[0].ParentCompactionId)
	assert.Equal(
		t,
		firstCompaction.ID,
		*compactions.Compactions[0].ParentCompactionId,
	)
	assert.Equal(t, firstCompaction.ID, compactions.Compactions[1].Id)

	compaction, err := fixture.runtime.GetSessionCompaction(
		context.Background(),
		first.SessionID,
		secondCompaction.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, secondCompaction.ID, compaction.Id)
	require.NotNil(t, compaction.ParentCompactionId)
	assert.Equal(t, firstCompaction.ID, *compaction.ParentCompactionId)
}

func apiMessageContents(messages []api.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}

	return contents
}
