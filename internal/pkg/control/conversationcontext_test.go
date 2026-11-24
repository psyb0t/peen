package control

import (
	"context"
	"testing"
	"time"

	"github.com/psyb0t/ctxscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationRPCContextPreservesScopeUntilServiceShutdown(t *testing.T) {
	t.Parallel()
	lifecycle, stopLifecycle := context.WithCancel(t.Context())
	t.Cleanup(stopLifecycle)
	conversation := &Conversation{feed: NewFeedPump(lifecycle, nil)}
	caller, stopCaller := context.WithCancel(ctxscope.Set(t.Context(),
		ctxscope.Attr("request_id", "caller-scope")))
	t.Cleanup(stopCaller)
	turn, stopTurn := conversation.turnContext(caller)
	t.Cleanup(stopTurn)
	assert.Equal(t, ctxscope.Get(caller), ctxscope.Get(turn))
	stopCaller()
	require.NoError(t, turn.Err())
	stopLifecycle()
	select {
	case <-turn.Done():
		require.ErrorIs(t, turn.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("service shutdown did not end the worker RPC context")
	}
	alreadyStopped, stop := conversation.turnContext(t.Context())
	t.Cleanup(stop)
	require.ErrorIs(t, alreadyStopped.Err(), context.Canceled)
}
