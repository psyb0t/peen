package mcpserver

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingNotifier struct {
	mu   sync.Mutex
	sent []*mcp.ProgressNotificationParams
	err  error
}

func (n *recordingNotifier) NotifyProgress(
	_ context.Context, params *mcp.ProgressNotificationParams,
) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, params)

	return n.err
}

func (n *recordingNotifier) messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	messages := make([]string, 0, len(n.sent))
	for _, params := range n.sent {
		messages = append(messages, params.Message)
	}

	return messages
}

func feedEvent(eventType, payload string) session.FeedItem {
	return session.FeedItem{
		Kind:        session.FeedKindEvent,
		EventType:   eventType,
		PayloadJSON: payload,
	}
}

func TestProgressThrottlesAndSendsTheLastEvent(t *testing.T) {
	t.Parallel()
	notifier := &recordingNotifier{}
	reporter := newProgressReporter(notifier, "token")
	clock := time.Unix(1000, 0)
	reporter.now = func() time.Time { return clock }
	ctx := t.Context()
	reporter.observe(ctx, feedEvent("turn.started", `{}`))
	clock = clock.Add(100 * time.Millisecond)
	reporter.observe(ctx, feedEvent("tool.use", `{"name":"readFile"}`))
	clock = clock.Add(100 * time.Millisecond)
	reporter.observe(ctx, feedEvent("tool.result", `{"content":"secret"}`))
	reporter.observe(ctx, feedEvent("tool.use", `{"name":"runCommand"}`))
	assert.Equal(t, []string{"turn.started"}, notifier.messages())
	clock = clock.Add(500 * time.Millisecond)
	reporter.observe(ctx, feedEvent("turn.completed", `{}`))
	assert.Equal(t, []string{"turn.started", "turn.completed"},
		notifier.messages())
	reporter.flush(ctx)
	assert.Len(t, notifier.messages(), 2)
	clock = clock.Add(time.Millisecond)
	reporter.observe(ctx, feedEvent("agent.run.tool.use", `{"name":"x"}`))
	reporter.flush(ctx)
	assert.Equal(t, []string{
		"turn.started", "turn.completed", "agent.run.tool.use x",
	}, notifier.messages())
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	progress := make([]float64, 0, len(notifier.sent))
	for _, params := range notifier.sent {
		assert.Equal(t, "token", params.ProgressToken)
		progress = append(progress, params.Progress)
	}
	// Progress counts every reported event, including throttled ones.
	assert.Equal(t, []float64{1, 4, 5}, progress)
}

func TestProgressSkipsTheThrottledMiddle(t *testing.T) {
	t.Parallel()
	notifier := &recordingNotifier{}
	reporter := newProgressReporter(notifier, 7)
	clock := time.Unix(1000, 0)
	reporter.now = func() time.Time { return clock }
	ctx := t.Context()
	reporter.observe(ctx, feedEvent("turn.started", `{}`))
	reporter.observe(ctx, feedEvent("tool.use", `{"name":"readFile"}`))
	reporter.observe(ctx, feedEvent("tool.use", `{"name":"editFile"}`))
	reporter.flush(ctx)
	assert.Equal(t, []string{"turn.started", "tool.use editFile"},
		notifier.messages())
}

func TestProgressSwallowsNotificationErrors(t *testing.T) {
	t.Parallel()
	notifier := &recordingNotifier{err: ctxerrors.New("client went away")}
	reporter := newProgressReporter(notifier, "token")
	clock := time.Unix(1000, 0)
	reporter.now = func() time.Time { return clock }
	assert.NotPanics(t, func() {
		reporter.observe(t.Context(), feedEvent("turn.started", `{}`))
		reporter.observe(t.Context(), feedEvent("turn.completed", `{}`))
		reporter.flush(t.Context())
	})
	assert.Len(t, notifier.messages(), 2)
}

func TestProgressMessages(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 150)
	for _, tc := range []struct {
		name    string
		item    session.FeedItem
		message string
		ok      bool
	}{
		{"tool", feedEvent("tool.use", `{"name":"runCommand",`+
			`"arguments":{"command":"secret"}}`), "tool.use runCommand", true},
		{"bad payload", feedEvent("tool.use", `nope`), "tool.use", true},
		{
			"turn", feedEvent("turn.failed", `{"error":"x"}`), "turn.failed",
			true,
		},
		{
			"child", feedEvent("agent.run.started", `{}`), "agent.run.started",
			true,
		},
		{"text", feedEvent("agent.run.text.delta", `{}`), "", false},
		{"result", feedEvent("tool.result", `{}`), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			message, ok := progressMessage(tc.item)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.message, message)
		})
	}
	message, ok := progressMessage(feedEvent("tool.use",
		`{"name":"`+long+`"}`))
	require.True(t, ok)
	assert.LessOrEqual(t, len(message), 200)
	assert.True(t, strings.HasPrefix(message, "tool.use é"))
	assert.Equal(t, message, strings.ToValidUTF8(message, "?"))
}

func TestSendMessageProgress(t *testing.T) {
	t.Parallel()
	events := make(chan bool, 1)
	boundary := &testBoundary{run: func(
		_ context.Context, request control.ConversationRequest,
	) (*control.ConversationResult, error) {
		events <- request.OnEvent != nil
		if request.OnEvent != nil {
			for _, item := range []session.FeedItem{
				feedEvent("turn.started", `{}`),
				feedEvent("tool.use", `{"name":"readFile"}`),
				feedEvent("turn.completed", `{}`),
			} {
				// A progress failure must never abort the turn.
				assert.NoError(t, request.OnEvent(item))
			}
		}

		return &control.ConversationResult{SessionID: uuid.New()}, nil
	}}
	received := make(chan *mcp.ProgressNotificationParams, 8)
	client := testClientWith(t, boundary.dependencies(), &mcp.ClientOptions{
		ProgressNotificationHandler: func(
			_ context.Context, request *mcp.ProgressNotificationClientRequest,
		) {
			received <- request.Params
		},
	})
	args := map[string]any{"workspace": "/workspace", "message": "hello"}
	callTool(t, client, toolSendMessage, args)
	assert.False(t, <-events)
	params := &mcp.CallToolParams{Name: toolSendMessage, Arguments: args}
	params.SetProgressToken("progress-1")
	result, err := client.CallTool(t.Context(), params)
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.True(t, <-events)
	// The first event goes out at once and the throttled last one is
	// flushed when the turn ends.
	messages := make([]string, 0, 3)
	for !slices.Contains(messages, "turn.completed") ||
		!slices.Contains(messages, "turn.started") {
		select {
		case notification := <-received:
			assert.Equal(t, "progress-1", notification.ProgressToken)
			messages = append(messages, notification.Message)
		case <-time.After(5 * time.Second):
			t.Fatalf("progress notifications missing, got %v", messages)
		}
	}
}
