// Package oneshot runs one turn from the command line and exits.
//
// A run reaches a controller, opens the session for a workspace, sends one
// message, and streams the session's frames until that message's turn ends.
// When no controller is running it starts one inside the process for the run
// and stops it afterwards; see Connect.
package oneshot

import (
	"bufio"
	"context"
	"errors"
	"io"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/ctxscope"
	"github.com/psyb0t/peen/internal/pkg/agent"
	controlclient "github.com/psyb0t/peen/internal/pkg/control/client"
	api "github.com/psyb0t/peen/internal/pkg/http/api"
	"github.com/psyb0t/peen/internal/pkg/http/wsapi"
	"github.com/psyb0t/peen/internal/pkg/worker"
)

const (
	// agentRunTypePrefix starts every child agent run frame type.
	agentRunTypePrefix = "agent.run."

	// answerPrompt asks for the answer to a question on stderr.
	answerPrompt = "answer> "

	// agentSpaceDockerShorthand is accepted for docker:default, the way
	// `peen setup --agent-space docker` accepts it.
	agentSpaceDockerShorthand = "docker"

	// agentSpaceMoveReason is recorded in the session's agent space history
	// when a run moves the session.
	agentSpaceMoveReason = "peen exec --agent-space"
)

// Options configures one run.
type Options struct {
	// Workspace is the absolute directory the session works in.
	Workspace string

	// AgentSpace is where the agent runs: control, docker, or
	// docker:<profile>, where docker means docker:default. A new session is
	// created in it, and an existing session in another agent space is moved
	// to it before the turn. Empty keeps the session's agent space, or the
	// controller's default for a new one.
	AgentSpace string

	// Message is the turn's message.send data: the prompt and the per-turn
	// model, reasoning effort, and system prompt.
	Message wsapi.MessageSendData

	Output OutputFormat
	Stdout io.Writer
	Stderr io.Writer

	// Answers supplies the user's answer to a question the agent asks, one
	// line per answer. Nil means no terminal is attached, and a question
	// cancels the turn with ErrQuestionUnanswerable.
	Answers io.Reader

	// Interrupt receives once for each interrupt. Before the turn starts it
	// abandons the run; during the turn it asks the controller to cancel the
	// turn, and the run keeps streaming until the controller confirms.
	Interrupt <-chan struct{}

	// Connect reaches the controller. Nil uses Connect.
	Connect ConnectFunc
}

// Run runs one turn and reports how it ended. It returns an error when the
// run could not reach a controller, open the session, or stream the turn,
// and ErrQuestionUnanswerable alongside the cancelled result when the agent
// asked a question nobody could answer.
func Run(
	ctx context.Context,
	options Options,
) (controlclient.TurnResult, error) {
	connect := options.Connect
	if connect == nil {
		connect = Connect
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	interrupts := &interruptRouter{abandon: cancelRun}

	var listener sync.WaitGroup
	defer listener.Wait()

	if options.Interrupt != nil {
		listener.Go(func() {
			interrupts.listen(runCtx, options.Interrupt)
		})
	}

	defer cancelRun()

	connection, err := connect(runCtx)
	if err != nil {
		return controlclient.TurnResult{}, ctxerrors.Wrap(
			err,
			"reach a controller",
		)
	}

	result, runErr := runTurn(runCtx, connection.Client, options, interrupts)

	// The controller outlives a cancelled run context long enough to stop
	// cleanly: its workers and SQLite close before Run returns.
	closeErr := connection.Close(context.WithoutCancel(ctx))
	if closeErr != nil {
		closeErr = ctxerrors.Wrap(closeErr, "stop the controller")
	}

	return result, errors.Join(runErr, closeErr)
}

func runTurn(
	ctx context.Context,
	client *controlclient.Client,
	options Options,
	interrupts *interruptRouter,
) (controlclient.TurnResult, error) {
	sessionID, err := openSession(ctx, client, options)
	if err != nil {
		return controlclient.TurnResult{}, err
	}

	ctx = ctxscope.Set(ctx, ctxscope.Attr("session_id", sessionID.String()))

	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	interrupts.during(func() {
		requestCancel(ctx, client, sessionID, "interrupt")
	})
	defer interrupts.during(nil)

	var workers sync.WaitGroup
	defer workers.Wait()

	questions := newQuestionHandler(
		client,
		sessionID,
		options,
		&workers,
	)
	output := newRenderer(options.Output, options.Stdout, options.Stderr)

	result, err := client.RunTurn(
		turnCtx,
		sessionID,
		options.Message,
		turnHandler(turnCtx, output, questions),
	)

	finishErr := output.finish()

	cancelTurn()

	if err != nil {
		// The stream stopped while the turn may still run: a second
		// interrupt, a closed output pipe, or a lost socket. Nobody is
		// watching it any more, so it is cancelled rather than left to run.
		requestCancel(ctx, client, sessionID, "stream_stopped")

		return controlclient.TurnResult{}, errors.Join(
			ctxerrors.Wrap(err, "run the turn"),
			finishErr,
		)
	}

	return result, errors.Join(finishErr, questions.err())
}

func turnHandler(
	ctx context.Context,
	output renderer,
	questions *questionHandler,
) controlclient.TurnHandler {
	return func(
		turn *controlclient.TurnConnection,
		frame controlclient.TurnFrame,
	) error {
		if err := output.render(frame); err != nil {
			return err
		}

		if frame.Type == agent.EventTypeQuestionAsked {
			questions.handle(ctx, turn)
		}

		return nil
	}
}

// openSession opens the workspace's session and moves it to the requested
// agent space when it runs somewhere else.
func openSession(
	ctx context.Context,
	client *controlclient.Client,
	options Options,
) (uuid.UUID, error) {
	agentSpace := NormalizeAgentSpace(options.AgentSpace)

	request := api.OpenSessionRequest{Workspace: options.Workspace}
	if agentSpace != "" {
		request.AgentSpace = &agentSpace
	}

	opened, err := client.OpenSession(ctx, request)
	if err != nil {
		return uuid.Nil, ctxerrors.Wrap(err, "open the workspace session")
	}

	sessionID := opened.Session.Id

	if agentSpace == "" || sessionAgentSpace(opened.Session) == agentSpace {
		return sessionID, nil
	}

	if _, err := client.ReconfigureSession(
		ctx,
		sessionID,
		api.ReconfigureSessionRequest{
			AgentSpace: agentSpace,
			Reason:     agentSpaceMoveReason,
		},
	); err != nil {
		return uuid.Nil, ctxerrors.Wrapf(
			err,
			"move the session to agent space %s",
			agentSpace,
		)
	}

	ctxscope.GetLogger(ctx).Info(
		"moved the session to another agent space",
		"session_id", sessionID.String(),
		"agent_space", agentSpace,
	)

	return sessionID, nil
}

// NormalizeAgentSpace expands the docker shorthand to docker:default and
// leaves every other name to the controller to check.
func NormalizeAgentSpace(name string) string {
	if name == agentSpaceDockerShorthand {
		return worker.DockerSpacePrefix + worker.DockerProfileDefault
	}

	return name
}

func sessionAgentSpace(session api.Session) string {
	if session.AgentSpace == nil {
		return ""
	}

	return *session.AgentSpace
}

// requestCancel asks the controller to cancel the session's turn. The cancel
// usually ends the turn before its response arrives, so the request must not
// share the turn's context; the client's own timeout bounds it.
func requestCancel(
	ctx context.Context,
	client *controlclient.Client,
	sessionID uuid.UUID,
	cause string,
) {
	if _, err := client.CancelSession(
		context.WithoutCancel(ctx),
		sessionID,
	); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"cancelling the turn failed",
			"reason", "cancel_request_failed",
			"cause", cause,
			"err", err,
		)
	}
}

// interruptRouter decides what an interrupt does at each point of a run.
// Before the turn, and after it, an interrupt abandons the run. While the turn
// runs, it asks the controller to cancel the turn instead.
type interruptRouter struct {
	abandon context.CancelFunc

	mutex  sync.Mutex
	inTurn func()
}

// during sets what an interrupt does while the turn runs. Nil goes back to
// abandoning the run.
func (r *interruptRouter) during(action func()) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.inTurn = action
}

func (r *interruptRouter) listen(
	ctx context.Context,
	interrupt <-chan struct{},
) {
	defer recoverWorker(ctx, "interrupt")

	for {
		select {
		case <-ctx.Done():
			return
		case <-interrupt:
		}

		r.mutex.Lock()
		action := r.inTurn
		r.mutex.Unlock()

		if action == nil {
			r.abandon()

			continue
		}

		action()
	}
}

// questionHandler answers the questions the root conversation asks during
// the turn, from the terminal when one is attached.
type questionHandler struct {
	client    *controlclient.Client
	sessionID uuid.UUID
	answers   io.Reader
	stderr    io.Writer
	workers   *sync.WaitGroup

	readerOnce sync.Once
	lines      chan answerLine

	mutex         sync.Mutex
	isUnanswered  bool
	isCancelAsked bool
}

// answerLine is one line read from the terminal, or why reading stopped.
type answerLine struct {
	text string
	err  error
}

func newQuestionHandler(
	client *controlclient.Client,
	sessionID uuid.UUID,
	options Options,
	workers *sync.WaitGroup,
) *questionHandler {
	return &questionHandler{
		client:    client,
		sessionID: sessionID,
		answers:   options.Answers,
		stderr:    options.Stderr,
		workers:   workers,
		lines:     make(chan answerLine),
	}
}

// handle starts answering one question. Waiting for the answer happens off
// the frame loop, so the session's other frames keep streaming meanwhile.
func (q *questionHandler) handle(
	ctx context.Context,
	turn *controlclient.TurnConnection,
) {
	if q.answers == nil {
		q.refuse(ctx)

		return
	}

	q.readerOnce.Do(func() { q.startReader(ctx) })

	q.workers.Go(func() {
		defer recoverWorker(ctx, "question")

		q.answer(ctx, turn)
	})
}

// startReader reads the terminal line by line for the rest of the process.
//
// A read from a terminal cannot be cancelled, so this goroutine is the one
// part of a run that does not wait for the turn: it stays blocked in a read
// until a line arrives or the process exits. Everything that waits for an
// answer selects on the turn's context as well.
func (q *questionHandler) startReader(ctx context.Context) {
	reader := bufio.NewReader(q.answers)

	go func() {
		defer recoverWorker(ctx, "answer reader")

		for {
			text, err := reader.ReadString('\n')
			q.lines <- answerLine{text: text, err: err}

			if err != nil {
				return
			}
		}
	}()
}

// refuse cancels the turn, because nobody can type the answer it waits for.
func (q *questionHandler) refuse(ctx context.Context) {
	q.mutex.Lock()
	q.isUnanswered = true
	isCancelAsked := q.isCancelAsked
	q.isCancelAsked = true
	q.mutex.Unlock()

	if isCancelAsked {
		return
	}

	if _, err := q.client.CancelSession(
		context.WithoutCancel(ctx),
		q.sessionID,
	); err != nil {
		ctxscope.GetLogger(ctx).Warn(
			"cancelling the turn after an unanswerable question failed",
			"reason", "cancel_request_failed",
			"err", err,
		)
	}
}

func (q *questionHandler) answer(
	ctx context.Context,
	turn *controlclient.TurnConnection,
) {
	logger := ctxscope.GetLogger(ctx)

	if _, err := io.WriteString(q.stderr, answerPrompt); err != nil {
		logger.Warn("writing the answer prompt failed", "err", err)
	}

	var line answerLine

	select {
	case <-ctx.Done():
		return
	case line = <-q.lines:
	}

	if line.err != nil && !errors.Is(line.err, io.EOF) {
		logger.Warn(
			"reading the answer failed",
			"reason", "answer_unreadable",
			"err", line.err,
		)

		q.refuse(ctx)

		return
	}

	answer := strings.TrimSpace(line.text)
	if answer == "" {
		q.refuse(ctx)

		return
	}

	if err := turn.Reply(answer); err != nil {
		logger.Warn(
			"sending the answer failed",
			"reason", "answer_not_sent",
			"err", err,
		)
	}
}

func (q *questionHandler) err() error {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if !q.isUnanswered {
		return nil
	}

	return ctxerrors.Wrap(ErrQuestionUnanswerable, "answer the agent")
}

// recoverWorker logs a panic in one of the run's goroutines instead of
// crashing the command mid-turn.
func recoverWorker(ctx context.Context, worker string) {
	recovered := recover()
	if recovered == nil {
		return
	}

	ctxscope.GetLogger(ctx).Error(
		"one-shot worker panicked",
		"worker", worker,
		"panic", recovered,
		"stack", string(debug.Stack()),
		"err", ctxerrors.Wrapf(commerr.ErrFailed, "%v", recovered),
	)
}
