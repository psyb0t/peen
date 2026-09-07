package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/essessey"
	essesseysse "github.com/psyb0t/essessey/sse"
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
		request api.MessageRequest
		want    TurnRequest
		wantErr error
	}{
		{
			name:    "minimal request",
			request: api.MessageRequest{Message: "inspect"},
			want: TurnRequest{
				Message:   "inspect",
				RequestID: requestID,
				SessionID: &sessionID,
			},
		},
		{
			name: "request with explicit settings",
			request: api.MessageRequest{
				Message:   "inspect",
				Workspace: new("/workspace"),
				Model:     new("provider/model"),
				SystemPrompt: &api.SystemPrompt{
					Mode:    api.SystemPromptModeAppend,
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
			request: api.MessageRequest{Message: " \t"},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "blank explicit workspace",
			request: api.MessageRequest{
				Message:   "inspect",
				Workspace: new(" \n"),
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "blank explicit model",
			request: api.MessageRequest{
				Message: "inspect",
				Model:   new(""),
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "blank system prompt content",
			request: api.MessageRequest{
				Message: "inspect",
				SystemPrompt: &api.SystemPrompt{
					Mode:    api.SystemPromptModeReplace,
					Content: "  ",
				},
			},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "unknown system prompt mode",
			request: api.MessageRequest{
				Message: "inspect",
				SystemPrompt: &api.SystemPrompt{
					Mode:    api.SystemPromptMode("unknown"),
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

func TestRuntimeAPISendsAndListsMessages(t *testing.T) {
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Text("first response"),
		elelemtest.Text("second response"),
	)
	fixture := newRuntimeFixture(t, driver)

	first, err := fixture.runtime.SendMessage(
		context.Background(),
		api.MessageRequest{Message: "first request"},
		nil,
		uuid.New(),
	)
	require.NoError(t, err)
	assert.Equal(t, "first response", first.Response.Message)

	second, err := fixture.runtime.SendMessage(
		context.Background(),
		api.MessageRequest{Message: "second request"},
		&first.SessionID,
		uuid.New(),
	)
	require.NoError(t, err)
	assert.Equal(t, first.SessionID, second.SessionID)
	assert.Equal(t, "second response", second.Response.Message)

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

func TestRuntimeAPIStreamsChatzCompatibleEvents(t *testing.T) {
	fixture := newRuntimeFixture(t, elelemtest.NewScriptedDriver(
		elelemtest.Thinking("inspect", "response"),
	))

	stream, err := fixture.runtime.StreamMessage(
		context.Background(),
		api.MessageRequest{Message: "stream request"},
		nil,
		uuid.New(),
	)
	require.NoError(t, err)

	body, err := io.ReadAll(stream.Body)
	require.NoError(t, err)
	require.NoError(t, stream.Body.Close())

	source := essesseysse.NewSource(bytes.NewReader(body))
	events := readSSEEvents(t, source)
	assert.Equal(
		t,
		[]essessey.EventType{
			// Advisory progress frames bracket the message. They are Chatz's
			// own event names, not Peen-specific wrappers, and a client that
			// only understands the seven content-block types ignores them.
			streamEventChatStatus,
			essessey.EventTypeMessageStart,
			essessey.EventTypePing,
			streamEventChatStatus,
			streamEventChatStatus,
			essessey.EventTypeContentBlockStart,
			essessey.EventTypeContentBlockDelta,
			essessey.EventTypeContentBlockStop,
			essessey.EventTypeContentBlockStart,
			essessey.EventTypeContentBlockDelta,
			essessey.EventTypeContentBlockStop,
			essessey.EventTypeMessageDelta,
			essessey.EventTypeMessageStop,
		},
		streamEventTypes(events),
	)
	assert.Equal(
		t,
		[]string{
			streamStatusConnecting,
			streamStatusWaitingFirstToken,
			streamStatusStreaming,
		},
		chatStatuses(t, events),
	)

	messages, err := fixture.store.ListMessages(
		context.Background(),
		stream.SessionID,
		session.ListMessagesOptions{Order: session.PageOrderAscending},
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"stream request", "response"},
		messageContents(messages.Items),
	)
}

func apiMessageContents(messages []api.Message) []string {
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}

	return contents
}

func readSSEEvents(t *testing.T, source essessey.Source) []essessey.Event {
	t.Helper()

	events := make([]essessey.Event, 0)
	for {
		event, err := source.Next(context.Background())
		if errors.Is(err, essessey.ErrNoMoreEvents) {
			return events
		}

		require.NoError(t, err)
		events = append(events, event)
	}
}

func streamEventTypes(events []essessey.Event) []essessey.EventType {
	types := make([]essessey.EventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Event)
	}

	return types
}

// chatStatuses returns the advisory progress values in wire order.
func chatStatuses(t *testing.T, events []essessey.Event) []string {
	t.Helper()

	statuses := make([]string, 0, len(events))

	for _, event := range events {
		if event.Event != streamEventChatStatus {
			continue
		}

		payload := chatStatusPayload{}
		require.NoError(t, json.Unmarshal(event.Data, &payload))
		statuses = append(statuses, payload.Status)
	}

	return statuses
}
