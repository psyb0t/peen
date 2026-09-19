// Package protocol carries Peen's private, session-bound worker protocol.
//
// One worker talks to its controller over one Unix socket. It registers with
// the credential its controller issued, asks for the durable records it needs,
// submits every event and state transition, and receives accepted work and
// cancellation. The controller validates the session and generation on every
// frame, writes each record to SQLite before publishing it, and answers with
// the durable result.
//
// The worker has no generic control API credential. It cannot read, write, or
// affect another session, because its socket is bound to one session and every
// frame is checked against the generation that registered on it.
package protocol

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

// Kind is the frame type. Both directions share one envelope so a single read
// loop on each side can dispatch every message.
type Kind string

const (
	// KindHello is the worker's first frame. Nothing else is accepted before
	// it.
	KindHello Kind = "hello"
	// KindHelloAck answers a registration attempt.
	KindHelloAck Kind = "hello_ack"
	// KindCall is a worker request for a durable operation.
	KindCall Kind = "call"
	// KindResult answers a call.
	KindResult Kind = "result"
	// KindCommand is controller-initiated work or control for the worker.
	KindCommand Kind = "command"
	// KindCommandResult answers a command.
	KindCommandResult Kind = "command_result"
)

// Command names the controller-initiated operations.
type Command string

const (
	// CommandRunTurn hands the worker one accepted user message to run.
	CommandRunTurn Command = "run_turn"
	// CommandCancel asks the worker to cancel its session's active turn.
	CommandCancel Command = "cancel"
	// CommandSignalJob asks the worker to signal one of its running jobs. The
	// job's process group lives in the worker, so the controller cannot reach
	// it and can only record what the worker reports back.
	CommandSignalJob Command = "signal_job"
	// CommandShutdown asks the worker to finish and exit.
	CommandShutdown Command = "shutdown"
)

// Frame is one message in either direction.
type Frame struct {
	Kind Kind `json:"kind"`

	// ID correlates a call or command with its answer. It is empty on hello.
	ID string `json:"id,omitempty"`

	// Method names the durable operation on a call frame, and the command on a
	// command frame.
	Method string `json:"method,omitempty"`

	Payload json.RawMessage `json:"payload,omitempty"`

	// Error is set on a result frame the operation failed.
	Error *Error `json:"error,omitempty"`
}

// Hello is the worker's registration payload.
//
// The credential is the only secret on the wire, and it travels once. A worker
// that names a session or generation its controller did not issue is refused
// and the connection is closed.
type Hello struct {
	SessionID    uuid.UUID         `json:"sessionId"`
	GenerationID uuid.UUID         `json:"generationId"`
	Credential   worker.Credential `json:"credential"`
	Profile      string            `json:"profile"`
}

// HelloAck confirms registration and tells the worker what its session already
// looks like, so it does not need a second round trip to start.
type HelloAck struct {
	SessionID uuid.UUID `json:"sessionId"`
	Workspace string    `json:"workspace"`
	Profile   string    `json:"profile"`
}

// RunTurn is the payload of a run_turn command.
//
// The controller has already recorded the accepted user message, so this
// carries the routing identity and the text, never a session the worker may
// choose.
type RunTurn struct {
	RequestID    uuid.UUID `json:"requestId"`
	Message      string    `json:"message"`
	Model        string    `json:"model,omitempty"`
	SystemPrompt string    `json:"systemPrompt,omitempty"`
	PromptMode   string    `json:"promptMode,omitempty"`
}

// TurnResult is the worker's answer to a run_turn command.
type TurnResult struct {
	Text   string `json:"text"`
	Queued bool   `json:"queued"`
}

// CancelResult is the worker's answer to a cancel command.
type CancelResult struct {
	CancelRequested bool `json:"cancelRequested"`
}

// SignalJob names one job the controller wants signalled.
type SignalJob struct {
	JobID  uuid.UUID `json:"jobId"`
	Signal string    `json:"signal"`
}

// SignalJobResult is the worker's answer to a signal command.
//
// Handled is false when this worker holds no such running job, which is not an
// error: the job may have exited between the read the caller acted on and this
// command arriving.
type SignalJobResult struct {
	Handled bool `json:"handled"`

	// Signalled is true when the worker found the job running and signalled it.
	Signalled bool `json:"signalled"`

	// State is the job's state at the moment it was signalled.
	State string `json:"state"`
}

// Ack answers an operation that succeeded and carries no value.
//
// It exists so a successful write is a real result rather than a nil pair,
// which would read the same as a handler that forgot to answer.
type Ack struct{}
