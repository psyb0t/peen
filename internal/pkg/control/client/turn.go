package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
)

const (
	// frameTypeMessageCompleted and frameTypeMessageFailed are the live-only
	// acknowledgements of one client frame. They carry the client frame's id
	// in triggeredBy.
	frameTypeMessageCompleted = "message.completed"
	frameTypeMessageFailed    = "message.failed"

	// turnTerminalGrace bounds the wait for a turn's last frame after the
	// controller already acknowledged the message. The acknowledgement and the
	// stored turn frames travel different paths, so either can arrive first.
	turnTerminalGrace = 30 * time.Second
)

// TurnOutcome is how a turn started by RunTurn ended.
type TurnOutcome string

const (
	TurnOutcomeCompleted TurnOutcome = "completed"
	TurnOutcomeFailed    TurnOutcome = "failed"
	TurnOutcomeCancelled TurnOutcome = "cancelled"
)

// TurnFrame is one server frame received while a turn runs.
type TurnFrame struct {
	// Raw is the frame exactly as the controller sent it.
	Raw json.RawMessage

	Type        string
	Data        json.RawMessage
	RequestID   string
	TriggeredBy string
}

// TurnResult reports how the turn ended. FailureCode and FailureMessage come
// from a message.failed acknowledgement and are empty otherwise.
type TurnResult struct {
	Outcome        TurnOutcome
	FailureCode    string
	FailureMessage string
}

// TurnHandler receives every frame of the session while the turn runs, in the
// order the socket delivered them. An error stops the turn's stream.
type TurnHandler func(turn *TurnConnection, frame TurnFrame) error

// TurnConnection is the socket a turn runs on. A handler uses it to send more
// messages to the same session, such as the answer to a question the turn
// asked.
type TurnConnection struct {
	sessionID  uuid.UUID
	connection *websocket.Conn
	writeMutex sync.Mutex
}

// Reply sends message to the session. While the turn runs, the controller
// queues it into the turn, which is how the model receives an answer.
func (t *TurnConnection) Reply(message string) error {
	return t.send(uuid.New(), wsapi.MessageSendData{Message: message})
}

func (t *TurnConnection) send(id uuid.UUID, data wsapi.MessageSendData) error {
	sessionID := t.sessionID.String()

	frame := wsapi.MessageSend{
		ID:       id.String(),
		Data:     data,
		Metadata: &wsapi.Metadata{SessionID: &sessionID},
	}

	t.writeMutex.Lock()
	defer t.writeMutex.Unlock()

	if err := t.connection.WriteJSON(frame); err != nil {
		return ctxerrors.Wrap(err, "send a message to the session")
	}

	return nil
}

// RunTurn sends one message to the session over a new socket and streams the
// session's frames to handler until that message's turn ends.
//
// The turn ends at the message's acknowledgement plus, when the message
// started a turn, that turn's turn.completed, turn.failed, or turn.cancelled
// frame. A message the controller queued into a turn already running ends
// with that turn. Frames of the session's other work, such as child agent
// runs, reach the handler too. A context cancel closes the socket; it does not
// cancel the turn on the controller.
func (c *Client) RunTurn(
	ctx context.Context,
	sessionID uuid.UUID,
	data wsapi.MessageSendData,
	handler TurnHandler,
) (TurnResult, error) {
	if sessionID == uuid.Nil {
		return TurnResult{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"a turn requires a session ID",
		)
	}

	connection, err := c.dialWebSocket(ctx, sessionID)
	if err != nil {
		return TurnResult{}, err
	}

	var closeOnce sync.Once

	closeConnection := func() {
		closeOnce.Do(func() {
			if closeErr := connection.Close(); closeErr != nil {
				ctxscope.GetLogger(ctx).Debug(
					"closing the turn socket failed",
					"err", closeErr,
				)
			}
		})
	}

	defer closeConnection()

	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	go func() {
		<-turnCtx.Done()
		closeConnection()
	}()

	turn := &TurnConnection{sessionID: sessionID, connection: connection}
	messageID := uuid.New()

	if err := turn.send(messageID, data); err != nil {
		return TurnResult{}, err
	}

	return streamTurn(ctx, turn, newTurnTracker(messageID.String()), handler)
}

// streamTurn reads frames until the tracker says the turn ended.
func streamTurn(
	ctx context.Context,
	turn *TurnConnection,
	tracker *turnTracker,
	handler TurnHandler,
) (TurnResult, error) {
	isGraceSet := false

	for {
		_, raw, err := turn.connection.ReadMessage()
		if err != nil {
			return turnReadFailure(ctx, tracker, err)
		}

		frame, err := decodeTurnFrame(raw)
		if err != nil {
			return TurnResult{}, err
		}

		if err := handler(turn, frame); err != nil {
			return TurnResult{}, ctxerrors.Wrap(err, "handle a turn frame")
		}

		tracker.observe(frame)

		if tracker.isDone() {
			return tracker.result(), nil
		}

		// A queued acknowledgement confirms receipt, not completion.
		// The running turn can continue beyond the terminal grace period.
		if isGraceSet || !tracker.isAcknowledged() || tracker.isQueued {
			continue
		}

		if err := turn.connection.SetReadDeadline(
			time.Now().Add(turnTerminalGrace),
		); err != nil {
			return TurnResult{}, ctxerrors.Wrap(
				err,
				"bound the wait for the turn's last frame",
			)
		}

		isGraceSet = true
	}
}

// turnReadFailure decides what a failed read means. A timeout after a
// non-queued acknowledgement means the last frame never came, so the
// acknowledgement alone reports the outcome.
func turnReadFailure(
	ctx context.Context,
	tracker *turnTracker,
	err error,
) (TurnResult, error) {
	if ctx.Err() != nil {
		return TurnResult{}, ctxerrors.Wrap(ctx.Err(), "run the turn")
	}

	var netErr net.Error
	if tracker.isAcknowledged() && !tracker.isQueued &&
		errors.As(err, &netErr) && netErr.Timeout() {
		ctxscope.GetLogger(ctx).Warn(
			"turn ended without its last frame",
			"reason", "terminal_frame_missing",
		)

		return tracker.result(), nil
	}

	return TurnResult{}, ctxerrors.Wrap(
		err,
		"read the session socket before the turn ended",
	)
}

// turnFrameEnvelope is the part of a server frame the turn tracking reads.
type turnFrameEnvelope struct {
	Type        string          `json:"type"`
	Data        json.RawMessage `json:"data"`
	TriggeredBy *string         `json:"triggeredBy"`
	Metadata    *struct {
		RequestID *string `json:"requestId"`
	} `json:"metadata"`
}

func decodeTurnFrame(raw []byte) (TurnFrame, error) {
	envelope := turnFrameEnvelope{}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return TurnFrame{}, ctxerrors.Wrap(err, "decode a session frame")
	}

	frame := TurnFrame{
		Raw:  json.RawMessage(raw),
		Type: envelope.Type,
		Data: envelope.Data,
	}

	if envelope.TriggeredBy != nil {
		frame.TriggeredBy = *envelope.TriggeredBy
	}

	if envelope.Metadata != nil && envelope.Metadata.RequestID != nil {
		frame.RequestID = *envelope.Metadata.RequestID
	}

	return frame, nil
}

// turnTracker follows one message from send to the end of its turn.
type turnTracker struct {
	messageID string

	hasAck    bool
	isQueued  bool
	requestID string
	failure   *wsapi.MessageFailedData

	started  map[string]bool
	terminal map[string]string

	// terminalAfterAck is the first turn end seen after the
	// acknowledgement. A queued message ends with the running turn, whose
	// request ID is not the message's own.
	terminalAfterAck string
}

func newTurnTracker(messageID string) *turnTracker {
	return &turnTracker{
		messageID: messageID,
		started:   make(map[string]bool),
		terminal:  make(map[string]string),
	}
}

func (t *turnTracker) observe(frame TurnFrame) {
	switch frame.Type {
	case frameTypeMessageCompleted:
		t.acknowledge(frame, func() {
			completed := wsapi.MessageCompletedData{}
			if json.Unmarshal(frame.Data, &completed) == nil {
				t.isQueued = completed.Queued
			}
		})
	case frameTypeMessageFailed:
		t.acknowledge(frame, func() {
			failed := wsapi.MessageFailedData{}
			if json.Unmarshal(frame.Data, &failed) == nil {
				t.failure = &failed
			}
		})
	case agent.EventTypeTurnStarted:
		t.started[frame.RequestID] = true
	case agent.EventTypeTurnCompleted,
		agent.EventTypeTurnFailed,
		agent.EventTypeTurnCancelled:
		t.terminal[frame.RequestID] = frame.Type

		if t.hasAck && t.terminalAfterAck == "" {
			t.terminalAfterAck = frame.Type
		}
	}
}

// acknowledge records the acknowledgement of this tracker's own message and
// ignores the ones answering other frames, such as a Reply.
func (t *turnTracker) acknowledge(frame TurnFrame, decode func()) {
	if frame.TriggeredBy != t.messageID || t.hasAck {
		return
	}

	t.hasAck = true
	t.requestID = frame.RequestID

	decode()
}

func (t *turnTracker) isAcknowledged() bool {
	return t.hasAck
}

func (t *turnTracker) isDone() bool {
	if !t.hasAck {
		return false
	}

	if t.isQueued {
		return t.terminalAfterAck != ""
	}

	if t.failure != nil && !t.started[t.requestID] {
		return true
	}

	// A cancel is final once acknowledged. The session's worker does not
	// always record turn.cancelled for a cancelled turn, so waiting for it
	// would only add the grace period.
	if t.failure != nil &&
		t.failure.Code == wsapi.MessageFailedCodeTurnCancelled {
		return true
	}

	return t.terminal[t.requestID] != ""
}

func (t *turnTracker) result() TurnResult {
	result := TurnResult{Outcome: TurnOutcomeCompleted}

	if t.failure != nil {
		result.Outcome = TurnOutcomeFailed
		result.FailureCode = string(t.failure.Code)
		result.FailureMessage = t.failure.Message

		if t.failure.Code == wsapi.MessageFailedCodeTurnCancelled {
			result.Outcome = TurnOutcomeCancelled
		}
	}

	terminal := t.terminal[t.requestID]
	if t.isQueued {
		terminal = t.terminalAfterAck
	}

	switch terminal {
	case agent.EventTypeTurnCompleted:
		result.Outcome = TurnOutcomeCompleted
	case agent.EventTypeTurnFailed:
		result.Outcome = TurnOutcomeFailed
	case agent.EventTypeTurnCancelled:
		result.Outcome = TurnOutcomeCancelled
	}

	return result
}
