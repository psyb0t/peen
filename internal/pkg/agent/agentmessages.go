package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/runtimecontext"
)

const (
	// messageToolTimeout replaces the per-tool timeout for waitMessages and
	// ask. Their own timeout argument bounds the wait, and an answer from a
	// person can take longer than any tool call should.
	messageToolTimeout = 30 * 24 * time.Hour

	agentMessageSummaryFormat  = "agent %q run %s sent a message"
	agentQuestionSummaryFormat = "agent %q run %s asked a question"
)

type sendMessageInput struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

type sendMessageOutput struct {
	To        string `json:"to"`
	Delivered bool   `json:"delivered"`
}

type waitMessagesInput struct {
	Timeout string `json:"timeout"`
}

// waitMessagesOutput says how many messages wait. They reach the
// conversation right after this tool result.
type waitMessagesOutput struct {
	Waiting  int  `json:"waiting"`
	TimedOut bool `json:"timedOut,omitempty"`
}

type stopAgentInput struct {
	AgentRunID uuid.UUID `json:"agentRunId"`
}

type stopAgentOutput struct {
	AgentRunID      uuid.UUID     `json:"agentRunId"`
	State           AgentRunState `json:"state"`
	CancelRequested bool          `json:"cancelRequested"`
}

type askInput struct {
	Question string `json:"question"`
	Timeout  string `json:"timeout"`
}

// askOutput says whether an answer arrived. The answer itself reaches the
// conversation right after this tool result.
type askOutput struct {
	Answered bool `json:"answered"`
	TimedOut bool `json:"timedOut,omitempty"`
}

// agentNoticeData is the data of an agent.message or agent.question event a
// child sends the root conversation.
type agentNoticeData struct {
	RunID     uuid.UUID `json:"runId"`
	AgentName string    `json:"agentName"`
	Message   string    `json:"message"`
}

type agentQuestionPayload struct {
	Question string `json:"question"`
}

type agentAnswerPayload struct {
	Answered bool `json:"answered"`
}

// messageArrival reports how many messages wait unread, and the channel that
// closes when the next one arrives.
type messageArrival func() (int, <-chan struct{})

// messageTools are the tools every conversation in a session uses to talk to
// the others. They touch nothing in the workspace, so an agent's
// allowed-tools never removes them.
func messageTools(
	deps *launchAgentDeps,
	onPostRun elelem.MessageInjector,
) []elelem.Tool {
	waitMessages := hostTool(
		toolNameWaitMessages,
		waitMessagesDescription,
		waitMessagesSchema,
		deps.waitMessages,
		onPostRun,
	)
	waitMessages.Timeout = messageToolTimeout

	ask := hostTool(
		toolNameAsk,
		askDescription,
		askSchema,
		deps.ask,
		onPostRun,
	)
	ask.Timeout = messageToolTimeout

	return []elelem.Tool{
		hostTool(
			toolNameSendMessage,
			sendMessageDescription,
			sendMessageSchema,
			deps.sendMessage,
			onPostRun,
		),
		waitMessages,
		hostTool(
			toolNameStopAgent,
			stopAgentDescription,
			stopAgentSchema,
			deps.stopAgent,
			onPostRun,
		),
		ask,
	}
}

// sendMessage delivers one message to a running child's inbox, or to the
// root conversation as an agent.message event.
func (d *launchAgentDeps) sendMessage(
	ctx context.Context,
	input sendMessageInput,
) (sendMessageOutput, error) {
	if strings.TrimSpace(input.Message) == "" {
		return sendMessageOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"message is required",
		)
	}

	sender := d.runtime.messageSender(ctx)
	to := strings.TrimSpace(input.To)

	if to == sender.Run {
		return sendMessageOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"an agent cannot send a message to itself",
		)
	}

	if to == rootAgentRunReference {
		if err := d.messageRoot(
			ctx,
			sender,
			events.TypeAgentMessage,
			agentMessageSummaryFormat,
			input.Message,
		); err != nil {
			return sendMessageOutput{}, err
		}

		return sendMessageOutput{To: to, Delivered: true}, nil
	}

	runID, err := uuid.Parse(to)
	if err != nil {
		return sendMessageOutput{}, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"to must be an agent run ID or %q, got %q",
			rootAgentRunReference,
			to,
		)
	}

	sender.Text = input.Message

	if err := d.deliverToRun(ctx, runID, sender); err != nil {
		return sendMessageOutput{}, err
	}

	return sendMessageOutput{To: to, Delivered: true}, nil
}

// deliverToRun tags message and puts it in a running child's inbox. A run
// this process does not hold is looked up durably, so the sender learns
// whether it ended or never existed.
func (d *launchAgentDeps) deliverToRun(
	ctx context.Context,
	runID uuid.UUID,
	message agentMessage,
) error {
	text, err := renderAgentMessage(message)
	if err != nil {
		return ctxerrors.Wrap(err, "tag agent message")
	}

	registry, err := d.runtime.sessionAgentRuns(d.sessionID)
	if err != nil {
		return err
	}

	err = registry.Deliver(ctx, runID, text)
	if err == nil {
		return nil
	}

	if !errors.Is(err, commerr.ErrNotFound) {
		return err
	}

	return d.runtime.unreachableAgentRun(ctx, d.sessionID, runID)
}

// messageAgentRun puts a person's message in a running child's inbox, tagged
// as coming from the user. It starts no turn, so it carries no turn settings.
func (r *Runtime) messageAgentRun(
	ctx context.Context,
	request MessageRequest,
) (*MessageRunResult, error) {
	if err := validateAgentRunMessage(request); err != nil {
		return nil, err
	}

	sessionID := r.sessionID
	if request.SessionID != nil {
		sessionID = *request.SessionID
	}

	text, err := renderAgentMessage(agentMessage{
		From: messageSenderUser,
		Text: request.Message,
	})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "tag the user's message")
	}

	runID := *request.To

	registry, err := r.sessionAgentRuns(sessionID)
	if err != nil {
		return nil, err
	}

	err = registry.Deliver(ctx, runID, text)
	if errors.Is(err, commerr.ErrNotFound) {
		err = r.unreachableAgentRun(ctx, sessionID, runID)
	}

	if err != nil {
		return nil, ctxerrors.Wrap(err, "message the agent run")
	}

	runtimecontext.Logger(ctx).Info(
		"user message queued for a child agent",
		"agent_run_id", runID.String(),
		"message_bytes", len(request.Message),
	)

	return &MessageRunResult{SessionID: sessionID, Queued: true}, nil
}

// validateAgentRunMessage refuses an empty message and any turn setting. A
// message to a child starts no turn, so a setting would have nothing to
// change.
func validateAgentRunMessage(request MessageRequest) error {
	if strings.TrimSpace(request.Message) == "" {
		return ctxerrors.Wrap(commerr.ErrValidationFailed, "message")
	}

	hasTurnSettings := request.Model != nil ||
		request.ReasoningEffort != nil ||
		request.SystemPrompt != nil ||
		request.Origin != nil
	if hasTurnSettings {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"a message to an agent cannot carry turn settings",
		)
	}

	return nil
}

// unreachableAgentRun explains why a run this process does not hold cannot
// take a message: it ended, or the session has no such run.
func (r *Runtime) unreachableAgentRun(
	ctx context.Context,
	sessionID uuid.UUID,
	runID uuid.UUID,
) error {
	stored, err := r.store.GetAgentRun(ctx, sessionID, runID)
	if errors.Is(err, commerr.ErrNotFound) {
		return ctxerrors.Wrapf(
			errAgentRunNotFound(),
			"no agent run %s in this session",
			runID,
		)
	}

	if err != nil {
		return ctxerrors.Wrapf(err, "find agent run %s", runID)
	}

	return ctxerrors.Wrapf(
		errAgentRunFinished(),
		"agent run %s is %s",
		runID,
		stored.State,
	)
}

// messageRoot sends a child's message or question to the root conversation as
// a session event that wakes an idle root. Its size is checked against the
// event bounds, so a child learns to shorten it instead of losing it.
func (d *launchAgentDeps) messageRoot(
	ctx context.Context,
	sender agentMessage,
	eventType events.Type,
	summaryFormat string,
	message string,
) error {
	parent, isChild := parentAgentRunFromContext(ctx)
	if !isChild {
		return ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"the root conversation cannot send a message to itself",
		)
	}

	publisher := d.runtime.durableEventPublisher()
	if publisher == nil {
		return ctxerrors.Wrap(ErrEventsUnavailable, "message the root")
	}

	data, err := json.Marshal(agentNoticeData{
		RunID:     parent.id,
		AgentName: sender.Name,
		Message:   message,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode agent message event")
	}

	summary := fmt.Sprintf(summaryFormat, sender.Name, parent.id)

	if err := d.runtime.eventLimits().Validate(summary, data); err != nil {
		return ctxerrors.Wrap(err, "fit the message in a session event")
	}

	if _, err := publisher.PublishContext(ctx, events.Notice{
		SessionID: d.sessionID,
		Type:      eventType,
		Source:    agentEventSource,
		Summary:   summary,
		Data:      data,
		Delivery:  events.DeliveryWake,
	}); err != nil {
		return ctxerrors.Wrap(err, "publish agent message event")
	}

	return nil
}

// waitMessages blocks until the caller has a message waiting: a child's
// inbox, or for the root the next user message or session event.
func (d *launchAgentDeps) waitMessages(
	ctx context.Context,
	input waitMessagesInput,
) (waitMessagesOutput, error) {
	timeout, err := optionalWaitTimeout(input.Timeout)
	if err != nil {
		return waitMessagesOutput{}, err
	}

	parent, isChild := parentAgentRunFromContext(ctx)
	if isChild {
		inbox, err := d.runInbox(parent.id)
		if err != nil {
			return waitMessagesOutput{}, err
		}

		return waitForMessages(ctx, timeout, inbox.arrival, nil, noNotices)
	}

	return d.waitRootMessages(ctx, timeout)
}

func (d *launchAgentDeps) waitRootMessages(
	ctx context.Context,
	timeout time.Duration,
) (waitMessagesOutput, error) {
	if d.userMessages == nil {
		return waitMessagesOutput{}, errNoRootQueue()
	}

	bus := d.runtime.eventBus
	if bus == nil {
		return waitForMessages(
			ctx,
			timeout,
			d.userMessages.arrival,
			nil,
			noNotices,
		)
	}

	notices, stop := bus.Subscribe(d.sessionID)
	defer stop()

	return waitForMessages(
		ctx,
		timeout,
		d.userMessages.arrival,
		notices,
		func() int { return bus.Pending(d.sessionID) },
	)
}

func noNotices() int {
	return 0
}

// errNoRootQueue reports a root conversation running without a turn queue,
// which only a caller that skipped the turn lifecycle can produce.
func errNoRootQueue() error {
	return ctxerrors.Wrap(
		commerr.ErrInvalidState,
		"the root conversation has no message queue",
	)
}

// waitForMessages blocks until something waits unread, the timeout passes, or
// ctx ends. A zero timeout waits until ctx ends.
func waitForMessages(
	ctx context.Context,
	timeout time.Duration,
	arrival messageArrival,
	notices <-chan events.Notice,
	pendingNotices func() int,
) (waitMessagesOutput, error) {
	var expired <-chan time.Time

	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		expired = timer.C
	}

	for {
		unread, arrived := arrival()

		waiting := unread + pendingNotices()
		if waiting > 0 {
			return waitMessagesOutput{Waiting: waiting}, nil
		}

		select {
		case <-arrived:
		case <-notices:
		case <-expired:
			return waitMessagesOutput{TimedOut: true}, nil
		case <-ctx.Done():
			return waitMessagesOutput{}, ctxerrors.Wrap(
				ctx.Err(),
				"wait for messages",
			)
		}
	}
}

// runInbox returns the inbox of a run this process holds.
func (d *launchAgentDeps) runInbox(runID uuid.UUID) (*agentInbox, error) {
	registry, err := d.runtime.sessionAgentRuns(d.sessionID)
	if err != nil {
		return nil, err
	}

	run, ok := registry.Get(runID)
	if !ok || run.inbox == nil {
		return nil, ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"agent run %s has no inbox",
			runID,
		)
	}

	return run.inbox, nil
}

// stopAgent cancels one running child. The child records its own
// cancellation as it ends.
func (d *launchAgentDeps) stopAgent(
	ctx context.Context,
	input stopAgentInput,
) (stopAgentOutput, error) {
	if input.AgentRunID == uuid.Nil {
		return stopAgentOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"agentRunId is required",
		)
	}

	if parent, isChild := parentAgentRunFromContext(ctx); isChild &&
		parent.id == input.AgentRunID {
		return stopAgentOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"an agent cannot stop itself, give a final answer instead",
		)
	}

	result, err := d.runtime.CancelSessionAgentRun(
		ctx,
		d.sessionID,
		input.AgentRunID,
	)
	if err != nil {
		return stopAgentOutput{}, err
	}

	return stopAgentOutput{
		AgentRunID:      result.AgentRunId,
		State:           string(result.State),
		CancelRequested: result.CancelRequested,
	}, nil
}

// ask sends a question to whoever the caller answers to and blocks until the
// next message reaches the caller. A child asks its parent. The root asks the
// user, whose next message is the answer.
func (d *launchAgentDeps) ask(
	ctx context.Context,
	input askInput,
) (askOutput, error) {
	if strings.TrimSpace(input.Question) == "" {
		return askOutput{}, ctxerrors.Wrap(
			commerr.ErrValidationFailed,
			"question is required",
		)
	}

	timeout, err := d.runtime.askTimeoutFor(input.Timeout)
	if err != nil {
		return askOutput{}, err
	}

	if _, isChild := parentAgentRunFromContext(ctx); isChild {
		return d.askParent(ctx, input.Question, timeout)
	}

	return d.askUser(ctx, input.Question, timeout)
}

// askParent records the question on the child's run, sends it to the run's
// parent, and waits on the child's own inbox.
func (d *launchAgentDeps) askParent(
	ctx context.Context,
	question string,
	timeout time.Duration,
) (askOutput, error) {
	asker, _ := parentAgentRunFromContext(ctx)

	registry, err := d.runtime.sessionAgentRuns(d.sessionID)
	if err != nil {
		return askOutput{}, err
	}

	run, ok := registry.Get(asker.id)
	if !ok || run.inbox == nil {
		return askOutput{}, ctxerrors.Wrapf(
			commerr.ErrInvalidState,
			"agent run %s has no inbox",
			asker.id,
		)
	}

	if err := run.inbox.sink.emit(
		ctx,
		EventTypeAgentRunQuestion,
		agentQuestionPayload{Question: question},
	); err != nil {
		return askOutput{}, ctxerrors.Wrap(err, "record agent question")
	}

	if err := d.sendQuestion(ctx, run, question); err != nil {
		d.recordAnswer(ctx, run, false)

		return askOutput{}, err
	}

	waited, err := waitForMessages(
		ctx,
		timeout,
		run.inbox.arrival,
		nil,
		noNotices,
	)

	d.recordAnswer(ctx, run, err == nil && !waited.TimedOut)

	if err != nil {
		return askOutput{}, err
	}

	return askOutput{Answered: !waited.TimedOut, TimedOut: waited.TimedOut}, nil
}

// sendQuestion routes a child's question to its parent: an agent.question
// event for the root, a question message in a parent run's inbox.
func (d *launchAgentDeps) sendQuestion(
	ctx context.Context,
	run *AgentRun,
	question string,
) error {
	sender := agentMessage{
		From: messageSenderAgent,
		Name: run.Name,
		Run:  run.ID.String(),
	}

	if run.ParentAgentRunID == nil {
		return d.messageRoot(
			ctx,
			sender,
			events.TypeAgentQuestion,
			agentQuestionSummaryFormat,
			question,
		)
	}

	sender.Kind = messageKindQuestion
	sender.Text = question

	return d.deliverToRun(ctx, *run.ParentAgentRunID, sender)
}

// recordAnswer closes the run's open question, so the run no longer reads as
// waiting.
func (d *launchAgentDeps) recordAnswer(
	ctx context.Context,
	run *AgentRun,
	answered bool,
) {
	if err := run.inbox.sink.emit(
		context.WithoutCancel(ctx),
		EventTypeAgentRunAnswer,
		agentAnswerPayload{Answered: answered},
	); err != nil {
		runtimecontext.Logger(ctx).Warn(
			"record agent answer",
			"agent_run_id", run.ID.String(),
			"err", err,
		)
	}
}

// askUser shows the root's question to the user on the turn's event stream
// and waits for the next user message, which joins the running turn.
func (d *launchAgentDeps) askUser(
	ctx context.Context,
	question string,
	timeout time.Duration,
) (askOutput, error) {
	if d.userMessages == nil {
		return askOutput{}, errNoRootQueue()
	}

	turn := d.userMessages.turn

	if err := turn.emit(
		ctx,
		EventTypeQuestionAsked,
		agentQuestionPayload{Question: question},
	); err != nil {
		return askOutput{}, ctxerrors.Wrap(err, "record root question")
	}

	waited, err := waitForMessages(
		ctx,
		timeout,
		d.userMessages.arrival,
		nil,
		noNotices,
	)

	answered := err == nil && !waited.TimedOut

	if emitErr := turn.emit(
		context.WithoutCancel(ctx),
		EventTypeQuestionAnswered,
		agentAnswerPayload{Answered: answered},
	); emitErr != nil {
		err = errors.Join(
			err,
			ctxerrors.Wrap(emitErr, "record root answer"),
		)
	}

	if err != nil {
		return askOutput{}, err
	}

	return askOutput{Answered: answered, TimedOut: waited.TimedOut}, nil
}

// askTimeoutFor resolves how long ask waits: the deployment's bound, which
// the call's own timeout may shorten. Zero waits without a bound.
func (r *Runtime) askTimeoutFor(raw string) (time.Duration, error) {
	requested, err := optionalWaitTimeout(raw)
	if err != nil {
		return 0, err
	}

	switch {
	case requested == 0:
		return r.askTimeout, nil
	case r.askTimeout == 0:
		return requested, nil
	default:
		return min(requested, r.askTimeout), nil
	}
}

// optionalWaitTimeout parses a wait bound given as a Go duration. Empty means
// no bound.
func optionalWaitTimeout(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}

	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"timeout %q must be a Go duration such as 90s, 5m, or 1h30m",
			raw,
		)
	}

	if timeout <= 0 {
		return 0, ctxerrors.Wrapf(
			commerr.ErrValidationFailed,
			"timeout %q must be positive",
			raw,
		)
	}

	return timeout, nil
}
