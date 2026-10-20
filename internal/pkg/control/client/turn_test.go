package client_test

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/ctxerrors/commerr"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const turnTestRequestID = "running-request"

type turnTestFrame struct {
	typ       string
	requestID string
	data      string
	ack       bool
}

func (f turnTestFrame) raw(t *testing.T, messageID string) []byte {
	t.Helper()
	frame := map[string]any{
		"type":     f.typ,
		"data":     json.RawMessage(f.data),
		"metadata": map[string]string{"requestId": f.requestID},
	}
	if f.ack {
		frame["triggeredBy"] = messageID
	}
	raw, err := json.MarshalIndent(frame, "", " ")
	require.NoError(t, err)

	return raw
}

func TestRunTurnFollowsTheAcknowledgedTurn(t *testing.T) {
	t.Parallel()
	started := turnTestFrame{
		typ:       "turn.started",
		requestID: turnTestRequestID,
		data:      `{}`,
	}
	terminal := turnTestFrame{
		typ:       "turn.completed",
		requestID: turnTestRequestID,
		data:      `{}`,
	}
	ack := turnTestFrame{
		typ:       "message.completed",
		requestID: turnTestRequestID,
		data:      `{"queued":false}`,
		ack:       true,
	}
	testCases := []struct {
		name        string
		frames      []turnTestFrame
		outcome     controlclient.TurnOutcome
		failureCode string
	}{
		{
			name:    "ack before terminal",
			frames:  []turnTestFrame{started, ack, terminal},
			outcome: controlclient.TurnOutcomeCompleted,
		},
		{
			name:    "ack after terminal",
			frames:  []turnTestFrame{started, terminal, ack},
			outcome: controlclient.TurnOutcomeCompleted,
		},
		{
			name: "queued follows another request",
			frames: []turnTestFrame{
				started,
				{
					typ:       "message.completed",
					requestID: "queued-request",
					data:      `{"queued":true}`,
					ack:       true,
				},
				terminal,
			},
			outcome: controlclient.TurnOutcomeCompleted,
		},
		{
			name: "failure without start",
			frames: []turnTestFrame{{
				typ:       "message.failed",
				requestID: turnTestRequestID,
				data:      `{"code":"VALIDATION_FAILED","message":"refused"}`,
				ack:       true,
			}},
			outcome:     controlclient.TurnOutcomeFailed,
			failureCode: "VALIDATION_FAILED",
		},
		{
			name: "cancel ack is final",
			frames: []turnTestFrame{
				started,
				{
					typ:       "message.failed",
					requestID: turnTestRequestID,
					data:      `{"code":"TURN_CANCELLED","message":"stopped"}`,
					ack:       true,
				},
			},
			outcome:     controlclient.TurnOutcomeCancelled,
			failureCode: "TURN_CANCELLED",
		},
		{
			name: "failed terminal",
			frames: []turnTestFrame{
				started,
				ack,
				{
					typ:       "turn.failed",
					requestID: turnTestRequestID,
					data:      `{}`,
				},
			},
			outcome: controlclient.TurnOutcomeFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sent := make(chan [][]byte, 1)
			stub := newAttachServer(t)
			stub.dials = []func(*websocket.Conn){func(
				connection *websocket.Conn,
			) {
				message := wsapi.MessageSend{}
				require.NoError(t, connection.ReadJSON(&message))
				assert.Equal(t, "prompt", message.Data.Message)
				rawFrames := make([][]byte, 0, len(tc.frames))
				for _, frame := range tc.frames {
					raw := frame.raw(t, message.ID)
					rawFrames = append(rawFrames, raw)
					require.NoError(t, connection.WriteMessage(
						websocket.TextMessage,
						raw,
					))
				}
				sent <- rawFrames
				// Wait for the client to decide the turn is final.
				_, _, _ = connection.ReadMessage()
			}}
			client := newAttachClient(t, stub.server(), "")
			ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
			defer cancel()
			received := [][]byte{}
			result, err := client.RunTurn(
				ctx,
				uuid.New(),
				wsapi.MessageSendData{Message: "prompt"},
				func(
					_ *controlclient.TurnConnection,
					frame controlclient.TurnFrame,
				) error {
					received = append(received, []byte(frame.Raw))

					return nil
				},
			)
			require.NoError(t, err)
			assert.Equal(t, tc.outcome, result.Outcome)
			assert.Equal(t, tc.failureCode, result.FailureCode)
			assert.Equal(t, <-sent, received)
		})
	}
}

func TestRunTurnReplySendsAnotherMessage(t *testing.T) {
	t.Parallel()
	sessionID := uuid.New()
	reply := make(chan wsapi.MessageSend, 1)
	stub := newAttachServer(t)
	stub.dials = []func(*websocket.Conn){func(connection *websocket.Conn) {
		initial := wsapi.MessageSend{}
		require.NoError(t, connection.ReadJSON(&initial))
		require.NoError(t, connection.WriteMessage(websocket.TextMessage,
			(turnTestFrame{typ: "question", data: `{}`}).raw(t, initial.ID)))
		answer := wsapi.MessageSend{}
		require.NoError(t, connection.ReadJSON(&answer))
		reply <- answer
		require.NoError(t, connection.WriteMessage(websocket.TextMessage,
			(turnTestFrame{
				typ: "message.failed", ack: true,
				data: `{"code":"TURN_CANCELLED","message":"done"}`,
			}).raw(t, initial.ID)))
	}}
	client := newAttachClient(t, stub.server(), "")
	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()
	_, err := client.RunTurn(
		ctx,
		sessionID,
		wsapi.MessageSendData{Message: "prompt"},
		func(
			turn *controlclient.TurnConnection,
			frame controlclient.TurnFrame,
		) error {
			if frame.Type == "question" {
				return turn.Reply("answer")
			}

			return nil
		},
	)
	require.NoError(t, err)
	got := <-reply
	assert.NotEmpty(t, got.ID)
	assert.Equal(t, "answer", got.Data.Message)
	require.NotNil(t, got.Metadata)
	require.NotNil(t, got.Metadata.SessionID)
	assert.Equal(t, sessionID.String(), *got.Metadata.SessionID)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"type":"message.send"`)
}

func TestRunTurnRejectsANilSession(t *testing.T) {
	t.Parallel()
	client, err := controlclient.New(controlclient.Options{
		ListenAddress: "127.0.0.1:0",
	})
	require.NoError(t, err)
	_, err = client.RunTurn(t.Context(), uuid.Nil, wsapi.MessageSendData{}, nil)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

type turnDeadlineConnection struct{ net.Conn }

func (c turnDeadlineConnection) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		deadline = time.Now()
	}

	return c.Conn.SetReadDeadline(deadline)
}

func TestRunTurnReadTimeoutAfterAckReturnsTheAckOutcome(t *testing.T) {
	t.Parallel()
	stub := newAttachServer(t)
	stub.dials = []func(*websocket.Conn){func(connection *websocket.Conn) {
		message := wsapi.MessageSend{}
		require.NoError(t, connection.ReadJSON(&message))
		frame := turnTestFrame{
			typ: "message.completed", requestID: turnTestRequestID,
			ack: true, data: `{"queued":false}`,
		}
		require.NoError(t, connection.WriteMessage(
			websocket.TextMessage,
			frame.raw(t, message.ID),
		))
		// Wait for the client to close after its read deadline expires.
		_, _, _ = connection.ReadMessage()
	}}
	server := stub.server()
	t.Cleanup(server.Close)
	client, err := controlclient.New(controlclient.Options{
		ListenAddress: strings.TrimPrefix(server.URL, "http://"),
		Dial: func(
			ctx context.Context,
			network, address string,
		) (net.Conn, error) {
			connection, dialErr := (&net.Dialer{}).DialContext(
				ctx,
				network,
				address,
			)
			if dialErr != nil {
				return nil, dialErr
			}

			return turnDeadlineConnection{Conn: connection}, nil
		},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()
	result, err := client.RunTurn(
		ctx,
		uuid.New(),
		wsapi.MessageSendData{Message: "prompt"},
		func(*controlclient.TurnConnection, controlclient.TurnFrame) error {
			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, controlclient.TurnOutcomeCompleted, result.Outcome)
}

type queuedTurnConnection struct {
	net.Conn
	acknowledged *atomic.Bool
	deadlines    *atomic.Int32
	reading      chan struct{}
}

func (c queuedTurnConnection) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		c.deadlines.Add(1)
		deadline = time.Now()
	}

	return c.Conn.SetReadDeadline(deadline)
}

func (c queuedTurnConnection) Read(data []byte) (int, error) {
	if c.acknowledged.Swap(false) {
		close(c.reading)
	}

	return c.Conn.Read(data)
}

func TestRunTurnQueuedAckDoesNotBoundTheRunningTurn(t *testing.T) {
	t.Parallel()
	var acknowledged atomic.Bool
	var deadlines atomic.Int32
	reading := make(chan struct{})
	sent := make(chan [][]byte, 1)
	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()
	stub := newAttachServer(t)
	stub.dials = []func(*websocket.Conn){func(connection *websocket.Conn) {
		message := wsapi.MessageSend{}
		require.NoError(t, connection.ReadJSON(&message))
		ack := (turnTestFrame{
			typ:       "message.completed",
			requestID: "queued-request",
			data:      `{"queued":true}`,
			ack:       true,
		}).raw(t, message.ID)
		terminal := (turnTestFrame{
			typ:       "turn.completed",
			requestID: turnTestRequestID,
			data:      `{}`,
		}).raw(t, message.ID)
		require.NoError(t, connection.WriteMessage(websocket.TextMessage, ack))
		select {
		case <-reading:
		case <-ctx.Done():
			return
		}
		sent <- [][]byte{ack, terminal}
		require.NoError(t, connection.WriteMessage(
			websocket.TextMessage,
			terminal,
		))
	}}
	server := stub.server()
	t.Cleanup(server.Close)
	client, err := controlclient.New(controlclient.Options{
		ListenAddress: strings.TrimPrefix(server.URL, "http://"),
		Dial: func(
			ctx context.Context,
			network, address string,
		) (net.Conn, error) {
			connection, dialErr := (&net.Dialer{}).DialContext(
				ctx,
				network,
				address,
			)
			if dialErr != nil {
				return nil, dialErr
			}

			return queuedTurnConnection{
				Conn:         connection,
				acknowledged: &acknowledged,
				deadlines:    &deadlines,
				reading:      reading,
			}, nil
		},
	})
	require.NoError(t, err)
	received := [][]byte{}
	result, err := client.RunTurn(
		ctx,
		uuid.New(),
		wsapi.MessageSendData{Message: "prompt"},
		func(
			_ *controlclient.TurnConnection,
			frame controlclient.TurnFrame,
		) error {
			received = append(received, []byte(frame.Raw))
			if frame.Type == "message.completed" {
				acknowledged.Store(true)
			}

			return nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, controlclient.TurnOutcomeCompleted, result.Outcome)
	assert.Zero(t, deadlines.Load())
	assert.Equal(t, <-sent, received)
}
