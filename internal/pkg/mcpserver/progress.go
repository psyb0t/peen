package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/session"
)

// progressNotifier is the part of an MCP server session that carries
// progress notifications back on the calling request.
type progressNotifier interface {
	NotifyProgress(context.Context, *mcp.ProgressNotificationParams) error
}

// progressReporter turns a sendMessage turn's events into throttled progress
// notifications. A notification names only the event type and tool name,
// never transcript text.
type progressReporter struct {
	notifier progressNotifier
	token    any
	now      func() time.Time

	mu      sync.Mutex
	count   int
	sent    time.Time
	pending *mcp.ProgressNotificationParams
}

// progressFor returns nil unless the caller asked for progress.
func progressFor(call *mcp.CallToolRequest) *progressReporter {
	if call == nil || call.Params == nil || call.Session == nil {
		return nil
	}

	token := call.Params.GetProgressToken()
	if token == nil {
		return nil
	}

	return newProgressReporter(call.Session, token)
}

func newProgressReporter(
	notifier progressNotifier,
	token any,
) *progressReporter {
	return &progressReporter{notifier: notifier, token: token, now: time.Now}
}

// observe sends at most one notification per progressInterval and keeps the
// newest skipped one for flush.
func (r *progressReporter) observe(ctx context.Context, item session.FeedItem) {
	message, ok := progressMessage(item)
	if !ok {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.count++

	params := &mcp.ProgressNotificationParams{
		ProgressToken: r.token,
		Progress:      float64(r.count),
		Message:       message,
	}

	now := r.now()
	if !r.sent.IsZero() && now.Sub(r.sent) < progressInterval {
		r.pending = params

		return
	}

	r.sent = now
	r.pending = nil
	r.send(ctx, params)
}

// flush sends the last skipped notification, so the client always sees the
// final event. A nil reporter does nothing.
func (r *progressReporter) flush(ctx context.Context) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	pending := r.pending
	if pending == nil {
		return
	}

	r.pending = nil
	r.send(ctx, pending)
}

// send never fails the turn: a client that stopped listening only loses the
// notification.
func (r *progressReporter) send(
	ctx context.Context,
	params *mcp.ProgressNotificationParams,
) {
	if err := r.notifier.NotifyProgress(ctx, params); err != nil {
		ctxscope.GetLogger(ctx).Debug("MCP progress notification failed",
			"error", err)
	}
}

func progressMessage(item session.FeedItem) (string, bool) {
	switch item.EventType {
	case agent.EventTypeToolUse, agent.EventTypeAgentRunToolUse:
		message := strings.TrimSpace(item.EventType + " " +
			toolName(item.PayloadJSON))

		return truncateProgress(message), true
	case agent.EventTypeTurnStarted, agent.EventTypeTurnCompleted,
		agent.EventTypeTurnFailed, agent.EventTypeTurnCancelled,
		agent.EventTypeAgentRunStarted:
		return item.EventType, true
	default:
		return "", false
	}
}

func toolName(payload string) string {
	use := struct {
		Name string `json:"name"`
	}{}
	if err := json.Unmarshal([]byte(payload), &use); err != nil {
		return ""
	}

	return use.Name
}

// truncateProgress cuts at a byte bound without splitting a character.
func truncateProgress(message string) string {
	if len(message) <= maxProgressMessageBytes {
		return message
	}

	return strings.ToValidUTF8(message[:maxProgressMessageBytes], "")
}
