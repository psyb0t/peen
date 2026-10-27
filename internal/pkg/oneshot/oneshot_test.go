package oneshot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

const oneshotTestTimeout = time.Minute

func TestNormalizeAgentSpace(t *testing.T) {
	t.Parallel()

	testCases := map[string]string{
		"": "", "control": "control", "docker": "docker:default",
		"docker:custom": "docker:custom", "invalid": "invalid",
	}
	for input, want := range testCases {
		assert.Equal(t, want, NormalizeAgentSpace(input))
	}
}

func TestInterruptRouterBeforeAndDuringTurn(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	abandoned, acted := make(chan struct{}, 1), make(chan struct{}, 1)
	router := &interruptRouter{abandon: func() { abandoned <- struct{}{} }}
	interrupt, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		router.listen(ctx, interrupt)
	}()
	t.Cleanup(func() { cancel(); <-done })
	interrupt <- struct{}{}
	waitOneshot(t, abandoned)
	router.during(func() { acted <- struct{}{} })
	interrupt <- struct{}{}
	waitOneshot(t, acted)
	router.during(nil)
	interrupt <- struct{}{}
	waitOneshot(t, abandoned)
}

func waitOneshot(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(oneshotTestTimeout):
		t.Fatal("timed out waiting for one-shot action")
	}
}

func TestRunReconfiguresOnlyDifferentAgentSpace(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		requested string
		existing  string
		moves     int32
	}{
		{name: "keep default", existing: "control"},
		{name: "same", requested: "control", existing: "control"},
		{name: "normalize", requested: "docker", existing: "docker:default"},
		{name: "move", requested: "docker", existing: "control", moves: 1},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := &oneshotStub{space: tc.existing}
			connection := stub.connection(t)
			var closed atomic.Bool
			result, err := Run(t.Context(), Options{
				Workspace: "/workspace", AgentSpace: tc.requested,
				Message: wsapi.MessageSendData{Message: "prompt"},
				Stdout:  io.Discard, Stderr: io.Discard,
				Connect: func(context.Context) (Connection, error) {
					return Connection{
						Client: connection,
						Close: func(context.Context) error {
							closed.Store(true)

							return nil
						},
					}, nil
				},
			})
			require.NoError(t, err)
			assert.Equal(t, controlclient.TurnOutcomeCompleted, result.Outcome)
			assert.Equal(t, tc.moves, stub.moves.Load())
			assert.True(t, closed.Load())
		})
	}
}

func TestRunUnanswerableQuestionCancelsAndCloses(t *testing.T) {
	t.Parallel()

	stub := &oneshotStub{space: "control", question: true}
	client := stub.connection(t)
	var closed atomic.Bool
	result, err := Run(t.Context(), Options{
		Workspace: "/workspace",
		Message:   wsapi.MessageSendData{Message: "prompt"},
		Stdout:    io.Discard, Stderr: io.Discard,
		Connect: func(context.Context) (Connection, error) {
			return Connection{Client: client, Close: func(context.Context) error {
				closed.Store(true)

				return nil
			}}, nil
		},
	})
	require.ErrorIs(t, err, ErrQuestionUnanswerable)
	assert.Equal(t, controlclient.TurnOutcomeCancelled, result.Outcome)
	assert.EqualValues(t, 1, stub.cancels.Load())
	assert.True(t, closed.Load())
}

func TestRunClosesAfterOpenFailure(t *testing.T) {
	t.Parallel()

	stub := &oneshotStub{failOpen: true}
	client := stub.connection(t)
	var closed atomic.Bool
	_, err := Run(t.Context(), Options{
		Workspace: "/workspace", Stdout: io.Discard, Stderr: io.Discard,
		Connect: func(context.Context) (Connection, error) {
			return Connection{Client: client, Close: func(ctx context.Context) error {
				closed.Store(ctx.Err() == nil)

				return commerr.ErrTerminated
			}}, nil
		},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, commerr.ErrTerminated)
	assert.True(t, closed.Load())
}

type oneshotStub struct {
	space     string
	question  bool
	failOpen  bool
	moves     atomic.Int32
	cancels   atomic.Int32
	cancelled chan struct{}
}

func (s *oneshotStub) connection(t *testing.T) *controlclient.Client {
	t.Helper()

	sessionID := uuid.New()
	s.cancelled = make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions/open", func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		if s.failOpen {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"code":"INTERNAL_ERROR","message":"failed"}`)

			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{"id": sessionID, "agentSpace": s.space},
		})
	})
	mux.HandleFunc("/api/v1/session/reconfigure", func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		s.moves.Add(1)
		request := struct {
			AgentSpace string `json:"agentSpace"`
		}{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		assert.Equal(t, "docker:default", request.AgentSpace)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	mux.HandleFunc("/api/v1/session/cancel", func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		s.cancels.Add(1)
		s.cancelled <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{}`)
	})
	mux.HandleFunc("/ws/v1", func(w http.ResponseWriter, r *http.Request) {
		s.socket(t, w, r)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client, err := controlclient.New(controlclient.Options{
		ListenAddress: strings.TrimPrefix(server.URL, "http://"),
		HTTPClient:    server.Client(),
	})
	require.NoError(t, err)

	return client
}

func (s *oneshotStub) socket(
	t *testing.T,
	w http.ResponseWriter,
	r *http.Request,
) {
	t.Helper()

	upgrader := websocket.Upgrader{
		Subprotocols: []string{"peen.v1"},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	socket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		t.Error(err)

		return
	}
	defer func() { _ = socket.Close() }()
	message := wsapi.MessageSend{}
	if err := socket.ReadJSON(&message); err != nil {
		t.Error(err)

		return
	}
	requestID := uuid.NewString()
	write := func(typ string, data any, triggeredBy string) {
		assert.NoError(t, socket.WriteJSON(map[string]any{
			"type": typ, "data": data, "triggeredBy": triggeredBy,
			"metadata": map[string]any{"requestId": requestID},
		}))
	}
	write("turn.started", map[string]any{}, "")
	if s.question {
		write("question.asked", map[string]any{"question": "continue?"}, "")
		select {
		case <-s.cancelled:
		case <-time.After(oneshotTestTimeout):
			t.Error("question did not cancel the turn")

			return
		}
		write("message.failed", map[string]any{
			"code": "TURN_CANCELLED", "message": "cancelled",
		}, message.ID)
		write("turn.cancelled", map[string]any{"reason": "cancelled"}, "")

		return
	}
	write("turn.completed", map[string]any{}, "")
	write("message.completed", map[string]any{"queued": false}, message.ID)
}
