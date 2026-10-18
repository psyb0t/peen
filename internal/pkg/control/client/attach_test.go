package client_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/ctxerrors/commerr"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	attachTestSubprotocol = "peen.v1"
	// attachTestBearerPrefix is a public protocol label, not a credential.
	//nolint:gosec // The token follows this prefix, the prefix is not secret.
	attachTestBearerPrefix = "peen.bearer."
	attachTestEventType    = "turn.completed"
	attachTestTimeout      = time.Minute
)

// attachFrame is one live socket frame, minimally shaped like a dabluveees
// event: an id, a type, a data payload, and optional sequence metadata.
type attachFrame struct {
	sequence *int64
	typ      string
	data     string
}

func (f attachFrame) encode(t *testing.T) []byte {
	t.Helper()

	payload := map[string]any{
		"id":   uuid.New().String(),
		"type": f.typ,
	}

	data := json.RawMessage(f.data)
	payload["data"] = data

	if f.sequence != nil {
		payload["metadata"] = map[string]any{"sequence": *f.sequence}
	}

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	return encoded
}

// attachServer stands in for the control API's feed surface: the WebSocket
// upgrade, GET /api/v1/session, and GET /api/v1/session/feed. Each test
// configures the pieces it needs; the rest answer harmless defaults.
type attachServer struct {
	t *testing.T

	mu           sync.Mutex
	feedSequence int64
	feedItems    []api.FeedFrame

	// dials, one func per connection in order. A test with no scripted dials
	// gets a default handler that just closes normally with no frames.
	dials []func(*websocket.Conn)

	dialCount       int
	gotSessionID    string
	gotSubprotocols []string

	feedCursors []int64
}

func newAttachServer(t *testing.T) *attachServer {
	t.Helper()

	return &attachServer{t: t}
}

func (s *attachServer) handshake() (string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.gotSessionID, s.gotSubprotocols
}

func (s *attachServer) requestedCursors() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]int64(nil), s.feedCursors...)
}

// commit appends an item the REST feed can replay, as if it had already been
// written before any client asked for it.
func (s *attachServer) commit(sequence int64, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	decoded := any(nil)
	require.NoError(s.t, json.Unmarshal([]byte(data), &decoded))

	s.feedItems = append(s.feedItems, api.FeedFrame{
		Sequence: sequence,
		Id:       uuid.New(),
		Type:     attachTestEventType,
		Data:     decoded,
	})
	if sequence > s.feedSequence {
		s.feedSequence = sequence
	}
}

func (s *attachServer) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/session", s.handleGetSession)
	mux.HandleFunc("/api/v1/session/feed", s.handleListFeed)
	mux.HandleFunc("/ws/v1", s.handleWebSocket)

	return httptest.NewServer(mux)
}

func (s *attachServer) handleGetSession(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	head := s.feedSequence
	s.mu.Unlock()

	writeJSON(s.t, w, api.Session{Id: uuid.New(), FeedSequence: head})
}

func (s *attachServer) handleListFeed(w http.ResponseWriter, r *http.Request) {
	cursor, err := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	require.NoError(s.t, err)

	s.mu.Lock()
	s.feedCursors = append(s.feedCursors, cursor)

	var items []api.FeedFrame
	for _, item := range s.feedItems {
		if item.Sequence > cursor {
			items = append(items, item)
		}
	}

	head := s.feedSequence
	s.mu.Unlock()

	nextCursor := cursor
	if len(items) > 0 {
		nextCursor = items[len(items)-1].Sequence
	}

	writeJSON(s.t, w, api.FeedPage{
		Items:      items,
		Limit:      100,
		NextCursor: nextCursor,
		HasMore:    false,
		Head:       head,
	})
}

func (s *attachServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.gotSessionID = r.URL.Query().Get("sessionId")
	s.gotSubprotocols = websocket.Subprotocols(r)
	index := s.dialCount
	s.dialCount++
	s.mu.Unlock()

	upgrader := websocket.Upgrader{
		Subprotocols: []string{attachTestSubprotocol},
		CheckOrigin:  func(*http.Request) bool { return true },
	}

	connection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = connection.Close() }()

	s.mu.Lock()
	var dial func(*websocket.Conn)
	if index < len(s.dials) {
		dial = s.dials[index]
	}
	s.mu.Unlock()

	if dial == nil {
		closeNormally(connection)

		return
	}

	dial(connection)
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(body))
}

func closeNormally(connection *websocket.Conn) {
	_ = connection.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
	)
}

// writeFrames sends each scripted frame as a live text message.
func writeFrames(t *testing.T, connection *websocket.Conn, frames []attachFrame) {
	t.Helper()

	for _, frame := range frames {
		require.NoError(t, connection.WriteMessage(
			websocket.TextMessage,
			frame.encode(t),
		))
	}
}

func newAttachClient(
	t *testing.T,
	server *httptest.Server,
	token string,
) *controlclient.Client {
	t.Helper()

	t.Cleanup(server.Close)

	client, err := controlclient.New(controlclient.Options{
		ListenAddress: strings.TrimPrefix(server.URL, "http://"),
		Token:         token,
		HTTPClient:    server.Client(),
	})
	require.NoError(t, err)

	return client
}

// Attaching asks the server for one session's events, so a client does not
// receive every session's feed and discard most of it.
func TestAttachRequestsTheServerSideSessionFilter(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	stub := newAttachServer(t)
	stub.dials = []func(*websocket.Conn){
		func(connection *websocket.Conn) {
			writeFrames(t, connection, []attachFrame{
				{sequence: new(int64(1)), typ: attachTestEventType, data: `{"ok":true}`},
			})
			closeNormally(connection)
		},
	}

	client := newAttachClient(t, stub.server(), testToken)

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	received := []controlclient.AttachEvent{}

	require.NoError(t, client.Attach(
		ctx,
		sessionID,
		nil,
		func(event controlclient.AttachEvent) error {
			received = append(received, event)

			return nil
		},
	))

	gotSessionID, _ := stub.handshake()
	assert.Equal(t, sessionID.String(), gotSessionID)

	require.Len(t, received, 1)
	assert.Equal(t, attachTestEventType, received[0].Type)
	assert.Contains(t, string(received[0].Data), "ok")
	require.NotNil(t, received[0].Sequence)
	assert.EqualValues(t, 1, *received[0].Sequence)
}

// A browser socket cannot set an Authorization header, so the token rides in
// the subprotocol list.
func TestAttachCarriesTheTokenAsASubprotocol(t *testing.T) {
	t.Parallel()

	stub := newAttachServer(t)
	client := newAttachClient(t, stub.server(), testToken)

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	require.NoError(t, client.Attach(
		ctx,
		uuid.New(),
		nil,
		func(controlclient.AttachEvent) error { return nil },
	))

	wantBearer := attachTestBearerPrefix +
		base64.RawURLEncoding.EncodeToString([]byte(testToken))

	_, gotSubprotocols := stub.handshake()
	assert.Contains(t, gotSubprotocols, attachTestSubprotocol)
	assert.Contains(t, gotSubprotocols, wantBearer)
}

// A deployment with no token sends no bearer subprotocol at all.
func TestAttachSendsNoBearerWithoutAToken(t *testing.T) {
	t.Parallel()

	stub := newAttachServer(t)
	client := newAttachClient(t, stub.server(), "")

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	require.NoError(t, client.Attach(
		ctx,
		uuid.New(),
		nil,
		func(controlclient.AttachEvent) error { return nil },
	))

	_, gotSubprotocols := stub.handshake()
	assert.Equal(t, []string{attachTestSubprotocol}, gotSubprotocols)
}

// Cancelling the context ends the stream without reporting a read failure.
func TestAttachStopsCleanlyWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	stub := newAttachServer(t)
	stub.dials = []func(*websocket.Conn){
		func(connection *websocket.Conn) {
			// Hold the socket open with no traffic so only cancellation ends
			// the attach.
			_, _, _ = connection.ReadMessage()
		},
	}

	client := newAttachClient(t, stub.server(), "")

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() {
		done <- client.Attach(
			ctx,
			uuid.New(),
			nil,
			func(controlclient.AttachEvent) error { return nil },
		)
	}()

	cancel()

	select {
	case attachErr := <-done:
		require.NoError(t, attachErr)
	case <-time.After(attachTestTimeout):
		t.Fatal("attach did not stop when its context ended")
	}
}

func TestAttachRejectsAMissingSessionID(t *testing.T) {
	t.Parallel()

	stub := newAttachServer(t)
	client := newAttachClient(t, stub.server(), testToken)

	err := client.Attach(
		t.Context(),
		uuid.Nil,
		nil,
		func(controlclient.AttachEvent) error { return nil },
	)
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

// A dropped connection reconnects, catches up exactly the frames missed while
// it was down, and then continues live. Nothing is replayed twice.
func TestAttachReconnectsAndCatchesUpExactlyOnce(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	stub := newAttachServer(t)

	var reconnected sync.WaitGroup
	reconnected.Add(1)

	stub.dials = []func(*websocket.Conn){
		func(connection *websocket.Conn) {
			writeFrames(t, connection, []attachFrame{
				{sequence: new(int64(1)), typ: attachTestEventType, data: `{"n":1}`},
			})

			// Commit rows 2 and 3 only after the drop, so they are reachable
			// solely through the REST catch-up this reconnect must perform.
			stub.commit(2, `{"n":2}`)
			stub.commit(3, `{"n":3}`)

			// An abrupt close, not a clean close frame, is what makes the
			// client treat this as a drop worth reconnecting for.
			_ = connection.UnderlyingConn().Close()
		},
		func(connection *websocket.Conn) {
			reconnected.Done()
			writeFrames(t, connection, []attachFrame{
				{sequence: new(int64(4)), typ: attachTestEventType, data: `{"n":4}`},
			})
			closeNormally(connection)
		},
	}

	client := newAttachClient(t, stub.server(), "")

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	var mu sync.Mutex
	received := []controlclient.AttachEvent{}

	done := make(chan error, 1)
	go func() {
		done <- client.Attach(
			ctx,
			sessionID,
			nil,
			func(event controlclient.AttachEvent) error {
				mu.Lock()
				received = append(received, event)
				mu.Unlock()

				return nil
			},
		)
	}()

	waitForGroup(t, &reconnected, attachTestTimeout)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(received) >= 4
	}, attachTestTimeout, 5*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	mu.Lock()
	defer mu.Unlock()

	sequences := make([]int64, 0, len(received))
	for _, event := range received {
		require.NotNil(t, event.Sequence)
		sequences = append(sequences, *event.Sequence)
	}
	assert.Equal(t, []int64{1, 2, 3, 4}, sequences)
}

// waitForGroup blocks until wg finishes or the timeout elapses.
func waitForGroup(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for the reconnect")
	}
}

// --cursor replays the feed from the given sequence before joining live.
func TestAttachCursorReplaysBeforeGoingLive(t *testing.T) {
	t.Parallel()

	stub := newAttachServer(t)
	stub.commit(1, `{"n":1}`)
	stub.commit(2, `{"n":2}`)

	stub.dials = []func(*websocket.Conn){
		func(connection *websocket.Conn) {
			writeFrames(t, connection, []attachFrame{
				{sequence: new(int64(3)), typ: attachTestEventType, data: `{"n":3}`},
			})
			closeNormally(connection)
		},
	}

	client := newAttachClient(t, stub.server(), "")

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	received := []controlclient.AttachEvent{}

	cursor := int64(0)
	require.NoError(t, client.Attach(
		ctx,
		uuid.New(),
		&cursor,
		func(event controlclient.AttachEvent) error {
			received = append(received, event)

			return nil
		},
	))

	require.Len(t, received, 3)

	sequences := make([]int64, 0, len(received))
	for _, event := range received {
		require.NotNil(t, event.Sequence)
		sequences = append(sequences, *event.Sequence)
	}
	assert.Equal(t, []int64{1, 2, 3}, sequences)

	// GetSession is never consulted when a cursor is given.
	cursors := stub.requestedCursors()
	require.NotEmpty(t, cursors)
	assert.EqualValues(t, 0, cursors[0])
}

// A live frame repeated over the socket is delivered only once.
func TestAttachSuppressesADuplicateLiveFrame(t *testing.T) {
	t.Parallel()

	stub := newAttachServer(t)
	stub.dials = []func(*websocket.Conn){
		func(connection *websocket.Conn) {
			frame := attachFrame{
				sequence: new(int64(1)), typ: attachTestEventType, data: `{"n":1}`,
			}
			writeFrames(t, connection, []attachFrame{frame, frame})
			closeNormally(connection)
		},
	}

	client := newAttachClient(t, stub.server(), "")

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	received := []controlclient.AttachEvent{}

	require.NoError(t, client.Attach(
		ctx,
		uuid.New(),
		nil,
		func(event controlclient.AttachEvent) error {
			received = append(received, event)

			return nil
		},
	))

	require.Len(t, received, 1)
	assert.EqualValues(t, 1, *received[0].Sequence)
}

// An unsequenced live acknowledgement, such as message.completed, carries no
// Sequence, which is what tells the caller to print it as an ack rather than
// advance its cursor.
func TestAttachMarksAnUnsequencedAck(t *testing.T) {
	t.Parallel()

	stub := newAttachServer(t)
	stub.dials = []func(*websocket.Conn){
		func(connection *websocket.Conn) {
			writeFrames(t, connection, []attachFrame{
				{typ: "message.completed", data: `{"queued":false}`},
			})
			closeNormally(connection)
		},
	}

	client := newAttachClient(t, stub.server(), "")

	ctx, cancel := context.WithTimeout(t.Context(), attachTestTimeout)
	defer cancel()

	received := []controlclient.AttachEvent{}

	require.NoError(t, client.Attach(
		ctx,
		uuid.New(),
		nil,
		func(event controlclient.AttachEvent) error {
			received = append(received, event)

			return nil
		},
	))

	require.Len(t, received, 1)
	assert.Nil(t, received[0].Sequence)
	assert.Equal(t, "message.completed", received[0].Type)
}
